// Package cloudtasks provides a postera.Enqueuer backed by GCP Cloud Tasks.
//
// An Enqueuer in this package translates each Posterum into an HTTP task
// targeting a single, pre-configured queue. Task names are derived
// deterministically from Posterum.ID, so postarius.Remove and the
// orchestrator's rollback paths address the same task they enqueued
// without round-tripping any provider-assigned identifier.
package cloudtasks

import (
	"context"
	"errors"
	"fmt"

	gcptasks "cloud.google.com/go/cloudtasks/apiv2"
	taskspb "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"go.naturallyfunny.dev/postera"
)

// Enqueuer schedules postera.Posterum entries as HTTP tasks in a single
// GCP Cloud Tasks queue.
//
// An Enqueuer is safe for concurrent use. It owns the underlying Cloud
// Tasks client; callers must invoke Close to release it when the value is
// no longer needed.
type Enqueuer struct {
	client              *gcptasks.Client
	queuePath           string
	targetURL           string
	serviceAccountEmail string
	headerMappings      []headerMapping
}

// headerMapping pairs a context key with the HTTP header that should
// receive its value on every dispatched task.
type headerMapping struct {
	ctxKey     any
	headerName string
}

// Option configures an Enqueuer at construction time.
type Option func(*Enqueuer)

// WithHeaderMapping registers a context-to-header mapping. On every
// Enqueue, the value at ctxKey is read from the request context; if it is
// present and of type string, it is added to the dispatched task's HTTP
// headers under headerName. Values that are absent or not strings are
// skipped silently.
//
// Multiple WithHeaderMapping options compose: each is evaluated on every
// Enqueue independently. Reading postera.NamespaceKey to project the
// orchestrator's namespace into a tenant-aware header (e.g. "x-user-id")
// is the canonical use case.
//
// WithHeaderMapping panics if ctxKey is nil or headerName is empty: a nil
// ctxKey would crash later inside context.Value, and an empty headerName
// would produce a malformed HTTP request. Surfacing the bug at the
// configuration site makes it impossible to ignore.
func WithHeaderMapping(ctxKey any, headerName string) Option {
	if ctxKey == nil {
		panic("cloudtasks: WithHeaderMapping called with nil ctxKey")
	}
	if headerName == "" {
		panic("cloudtasks: WithHeaderMapping called with empty headerName")
	}
	return func(e *Enqueuer) {
		e.headerMappings = append(e.headerMappings, headerMapping{
			ctxKey:     ctxKey,
			headerName: headerName,
		})
	}
}

// NewEnqueuer returns an Enqueuer that targets the queue identified by
// projectID, locationID, and queueID and dispatches POST requests to
// targetURL.
//
// serviceAccountEmail, when non-empty, is used to mint an OIDC token on
// every dispatched task — required when the target is a protected endpoint
// such as Cloud Run or Cloud Functions. An empty value disables OIDC and
// dispatches unauthenticated requests; this is the only field whose
// emptiness is interpreted as opt-out rather than misconfiguration.
//
// projectID, locationID, queueID, and targetURL must be non-empty.
// NewEnqueuer creates an underlying Cloud Tasks client using ctx and the
// host's default credentials; if the client fails to initialize, the error
// is returned without partial state.
func NewEnqueuer(ctx context.Context, projectID, locationID, queueID, targetURL, serviceAccountEmail string, opts ...Option) (*Enqueuer, error) {
	if projectID == "" || locationID == "" || queueID == "" {
		return nil, errors.New("cloudtasks: projectID, locationID, and queueID must be non-empty")
	}
	if targetURL == "" {
		return nil, errors.New("cloudtasks: targetURL must be non-empty")
	}

	client, err := gcptasks.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("cloudtasks: new client: %w", err)
	}

	e := &Enqueuer{
		client:              client,
		queuePath:           fmt.Sprintf("projects/%s/locations/%s/queues/%s", projectID, locationID, queueID),
		targetURL:           targetURL,
		serviceAccountEmail: serviceAccountEmail,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e, nil
}

// Close releases the underlying Cloud Tasks client.
func (e *Enqueuer) Close() error {
	return e.client.Close()
}

// Enqueue schedules p as a Cloud Tasks HTTP task whose name is
// deterministic in p.ID:
//
//	projects/{projectID}/locations/{locationID}/queues/{queueID}/tasks/{p.ID}
//
// allowing Cancel to address the same task by id alone. The task is
// created with ScheduleTime set to p.ExecuteAt and an HttpRequest carrying
// p.Body as its POST body. When a Service Account email was configured,
// the request is signed with an OIDC token; any configured header mappings
// are evaluated against ctx and merged into the task's HTTP headers.
//
// Enqueue is idempotent on codes.AlreadyExists: when a task with the same
// name is already present in the queue, the call returns nil so that
// orchestrator-level rollback paths re-enqueueing an in-flight id are not
// surfaced as failures. ctx is forwarded to the underlying client so that
// caller-side cancellation and timeouts are respected.
func (e *Enqueuer) Enqueue(ctx context.Context, p postera.Posterum) error {
	httpReq := &taskspb.HttpRequest{
		Url:        e.targetURL,
		HttpMethod: taskspb.HttpMethod_POST,
		Body:       p.Body,
		Headers:    e.headersFromContext(ctx),
	}
	if e.serviceAccountEmail != "" {
		httpReq.AuthorizationHeader = &taskspb.HttpRequest_OidcToken{
			OidcToken: &taskspb.OidcToken{
				ServiceAccountEmail: e.serviceAccountEmail,
			},
		}
	}

	req := &taskspb.CreateTaskRequest{
		Parent: e.queuePath,
		Task: &taskspb.Task{
			Name:         e.taskName(p.ID),
			ScheduleTime: timestamppb.New(p.ExecuteAt),
			MessageType: &taskspb.Task_HttpRequest{
				HttpRequest: httpReq,
			},
		},
	}

	if _, err := e.client.CreateTask(ctx, req); err != nil {
		if status.Code(err) == codes.AlreadyExists {
			return nil
		}
		return fmt.Errorf("cloudtasks: create task %s: %w", p.ID, err)
	}
	return nil
}

// Cancel deletes the task whose name is derived from id, mirroring the
// deterministic name produced by Enqueue.
//
// Cancel is best-effort and idempotent on codes.NotFound: if the task has
// already fired, has been deleted, or never existed, Cancel returns nil so
// that callers can safely retry without distinguishing those cases. ctx is
// forwarded to the underlying client so that caller-side cancellation and
// timeouts are respected.
func (e *Enqueuer) Cancel(ctx context.Context, id string) error {
	req := &taskspb.DeleteTaskRequest{
		Name: e.taskName(id),
	}
	if err := e.client.DeleteTask(ctx, req); err != nil {
		if status.Code(err) == codes.NotFound {
			return nil
		}
		return fmt.Errorf("cloudtasks: delete task %s: %w", id, err)
	}
	return nil
}

// taskName returns the fully-qualified Cloud Tasks resource name for id
// under the configured queue.
func (e *Enqueuer) taskName(id string) string {
	return fmt.Sprintf("%s/tasks/%s", e.queuePath, id)
}

// headersFromContext evaluates the configured header mappings against ctx
// and returns a header map suitable for taskspb.HttpRequest.Headers.
//
// A mapping is skipped when its context value is absent or not a string;
// nil is returned when no mappings are configured so that the common path
// pays for no allocation.
func (e *Enqueuer) headersFromContext(ctx context.Context) map[string]string {
	if len(e.headerMappings) == 0 {
		return nil
	}
	headers := make(map[string]string, len(e.headerMappings))
	for _, m := range e.headerMappings {
		if v, ok := ctx.Value(m.ctxKey).(string); ok {
			headers[m.headerName] = v
		}
	}
	return headers
}

var _ postera.Enqueuer = (*Enqueuer)(nil)
