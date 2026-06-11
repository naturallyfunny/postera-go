package cloudtasks

import (
	"context"
	"errors"
	"fmt"

	taskspb "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"go.naturallyfunny.dev/postera"
)

type Config struct {
	ProjectID           string
	LocationID          string
	QueueID             string
	TargetURL           string
	ServiceAccountEmail string
}

type tasksClient interface {
	CreateTask(ctx context.Context, req *taskspb.CreateTaskRequest, opts ...gax.CallOption) (*taskspb.Task, error)
	DeleteTask(ctx context.Context, req *taskspb.DeleteTaskRequest, opts ...gax.CallOption) error
}

type Enqueuer struct {
	client              tasksClient
	queuePath           string
	targetURL           string
	serviceAccountEmail string
	humanHeader         string
	agentHeader         string
	sessionHeader       string
	metadataHeaders     []metadataHeader
}

type metadataHeader struct {
	key        string
	headerName string
}

type Option func(*Enqueuer)

func WithHumanHeader(headerName string) Option {
	if headerName == "" {
		panic("cloudtasks: WithHumanHeader called with empty headerName")
	}
	return func(e *Enqueuer) {
		e.humanHeader = headerName
	}
}

func WithAgentHeader(headerName string) Option {
	if headerName == "" {
		panic("cloudtasks: WithAgentHeader called with empty headerName")
	}
	return func(e *Enqueuer) {
		e.agentHeader = headerName
	}
}

func WithSessionHeader(headerName string) Option {
	if headerName == "" {
		panic("cloudtasks: WithSessionHeader called with empty headerName")
	}
	return func(e *Enqueuer) {
		e.sessionHeader = headerName
	}
}

func WithMetadataHeader(key string, headerName string) Option {
	if key == "" {
		panic("cloudtasks: WithMetadataHeader called with empty key")
	}
	if headerName == "" {
		panic("cloudtasks: WithMetadataHeader called with empty headerName")
	}
	return func(e *Enqueuer) {
		e.metadataHeaders = append(e.metadataHeaders, metadataHeader{
			key:        key,
			headerName: headerName,
		})
	}
}

func NewEnqueuer(client tasksClient, cfg Config, opts ...Option) (*Enqueuer, error) {
	if cfg.ProjectID == "" || cfg.LocationID == "" || cfg.QueueID == "" {
		return nil, errors.New("cloudtasks: ProjectID, LocationID, and QueueID must be non-empty")
	}
	// TargetURL is validated lazily in Enqueue; cancel-only consumers do not need it.

	e := &Enqueuer{
		client:              client,
		queuePath:           fmt.Sprintf("projects/%s/locations/%s/queues/%s", cfg.ProjectID, cfg.LocationID, cfg.QueueID),
		targetURL:           cfg.TargetURL,
		serviceAccountEmail: cfg.ServiceAccountEmail,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e, nil
}

func (e *Enqueuer) Enqueue(ctx context.Context, p postera.Posterum) error {
	if e.targetURL == "" {
		return errors.New("cloudtasks: enqueue requires a non-empty TargetURL")
	}
	httpReq := &taskspb.HttpRequest{
		Url:        e.targetURL,
		HttpMethod: taskspb.HttpMethod_POST,
		Body:       []byte(p.Message),
		Headers:    e.headersFromPosterum(p),
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
			ScheduleTime: timestamppb.New(p.TriggerAt),
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

func (e *Enqueuer) taskName(id string) string {
	return fmt.Sprintf("%s/tasks/%s", e.queuePath, id)
}

func (e *Enqueuer) headersFromPosterum(p postera.Posterum) map[string]string {
	headers := make(map[string]string)
	if e.humanHeader != "" && p.Human != "" {
		headers[e.humanHeader] = p.Human
	}
	if e.agentHeader != "" && p.Agent != "" {
		headers[e.agentHeader] = p.Agent
	}
	if e.sessionHeader != "" && p.Session != "" {
		headers[e.sessionHeader] = p.Session
	}
	for _, m := range e.metadataHeaders {
		if v, ok := p.Metadata[m.key]; ok && v != "" {
			headers[m.headerName] = v
		}
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

var _ postera.Enqueuer = (*Enqueuer)(nil)
