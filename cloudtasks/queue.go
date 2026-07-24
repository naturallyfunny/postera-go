package cloudtasks

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	cloudtaskspkg "cloud.google.com/go/cloudtasks/apiv2"
	taskspb "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	gax "github.com/googleapis/gax-go/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"go.naturallyfunny.dev/postera"
)

const (
	// minLead is the smallest lead time Enqueue accepts. Scheduling closer than
	// this is racy: a task can be dispatched before upstream state that produced
	// it is durably written.
	minLead = 29 * time.Second
	// maxLead is the largest lead time Enqueue accepts. Cloud Tasks rejects a
	// scheduleTime more than 30 days out; 29 days leaves margin for the gap
	// between this check and CreateTask actually reaching the platform.
	maxLead = 29 * 24 * time.Hour
)

type headerMapping struct {
	headerName string
	get        func(postera.Posterum) string
}

// taskClient is the subset of *cloudtasks.Client that Queue uses, letting tests
// substitute a fake.
type taskClient interface {
	CreateTask(context.Context, *taskspb.CreateTaskRequest, ...gax.CallOption) (*taskspb.Task, error)
	DeleteTask(context.Context, *taskspb.DeleteTaskRequest, ...gax.CallOption) error
}

type Queue struct {
	client              taskClient
	queuePath           string
	targetURL           string
	serviceAccountEmail string
	headers             []headerMapping
	maxAttempts         int
	baseDelay           time.Duration
}

type Option func(*Queue) error

func WithTargetURL(targetURL string) Option {
	return func(q *Queue) error {
		if targetURL == "" {
			return errors.New("cloudtasks: WithTargetURL: empty url")
		}
		u, err := url.Parse(targetURL)
		if err != nil {
			return fmt.Errorf("cloudtasks: WithTargetURL: %q is not a valid URL: %w", targetURL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("cloudtasks: WithTargetURL: %q must use an http or https scheme", targetURL)
		}
		if u.Host == "" {
			return fmt.Errorf("cloudtasks: WithTargetURL: %q must include a host", targetURL)
		}
		q.targetURL = targetURL
		return nil
	}
}

// WithRetry retries transient gRPC failures (Unavailable, DeadlineExceeded,
// ResourceExhausted, Aborted) up to maxAttempts times, doubling baseDelay
// between tries. Off by default.
func WithRetry(maxAttempts int, baseDelay time.Duration) Option {
	return func(q *Queue) error {
		if maxAttempts < 1 {
			return fmt.Errorf("cloudtasks: WithRetry: maxAttempts must be >= 1, got %d", maxAttempts)
		}
		q.maxAttempts = maxAttempts
		q.baseDelay = baseDelay
		return nil
	}
}

func WithServiceAccountEmail(email string) Option {
	return func(q *Queue) error {
		if email == "" {
			return errors.New("cloudtasks: WithServiceAccountEmail: empty email")
		}
		q.serviceAccountEmail = email
		return nil
	}
}

func WithHumanHeader(headerName string) Option {
	return func(q *Queue) error {
		if headerName == "" {
			return errors.New("cloudtasks: WithHumanHeader: empty headerName")
		}
		q.headers = append(q.headers, headerMapping{
			headerName: headerName,
			get:        func(p postera.Posterum) string { return p.Human },
		})
		return nil
	}
}

func WithAgentHeader(headerName string) Option {
	return func(q *Queue) error {
		if headerName == "" {
			return errors.New("cloudtasks: WithAgentHeader: empty headerName")
		}
		q.headers = append(q.headers, headerMapping{
			headerName: headerName,
			get:        func(p postera.Posterum) string { return p.Agent },
		})
		return nil
	}
}

func WithSessionHeader(headerName string) Option {
	return func(q *Queue) error {
		if headerName == "" {
			return errors.New("cloudtasks: WithSessionHeader: empty headerName")
		}
		q.headers = append(q.headers, headerMapping{
			headerName: headerName,
			get:        func(p postera.Posterum) string { return p.Session },
		})
		return nil
	}
}

func WithMetadataHeader(key, headerName string) Option {
	return func(q *Queue) error {
		if key == "" {
			return errors.New("cloudtasks: WithMetadataHeader: empty key")
		}
		if headerName == "" {
			return errors.New("cloudtasks: WithMetadataHeader: empty headerName")
		}
		q.headers = append(q.headers, headerMapping{
			headerName: headerName,
			get:        func(p postera.Posterum) string { return p.Metadata[key] },
		})
		return nil
	}
}

func WithFixedHeader(name, value string) Option {
	return func(q *Queue) error {
		if name == "" {
			return errors.New("cloudtasks: WithFixedHeader: empty name")
		}
		if value == "" {
			return errors.New("cloudtasks: WithFixedHeader: empty value")
		}
		q.headers = append(q.headers, headerMapping{
			headerName: name,
			get:        func(_ postera.Posterum) string { return value },
		})
		return nil
	}
}

func NewQueue(client *cloudtaskspkg.Client, project, location, queue string, opts ...Option) (*Queue, error) {
	if project == "" || location == "" || queue == "" {
		return nil, errors.New("cloudtasks: project, location, and queue must be non-empty")
	}

	q := &Queue{
		client:    client,
		queuePath: fmt.Sprintf("projects/%s/locations/%s/queues/%s", project, location, queue),
	}
	for _, opt := range opts {
		if err := opt(q); err != nil {
			return nil, err
		}
	}
	return q, nil
}

func (q *Queue) Enqueue(ctx context.Context, p postera.Posterum) error {
	if q.targetURL == "" {
		return errors.New("cloudtasks: enqueue: no target URL configured; use WithTargetURL")
	}
	if lead := time.Until(p.TriggerAt); lead <= minLead || lead >= maxLead {
		return fmt.Errorf("cloudtasks: enqueue %s: trigger %s is %s from now, must be within (%s, %s): %w",
			p.ID, p.TriggerAt, lead, minLead, maxLead, postera.ErrScheduleOutOfRange)
	}
	httpReq := &taskspb.HttpRequest{
		Url:        q.targetURL,
		HttpMethod: taskspb.HttpMethod_POST,
		Body:       []byte(p.Message),
		Headers:    q.headersFromPosterum(p),
	}
	if q.serviceAccountEmail != "" {
		httpReq.AuthorizationHeader = &taskspb.HttpRequest_OidcToken{
			OidcToken: &taskspb.OidcToken{
				ServiceAccountEmail: q.serviceAccountEmail,
			},
		}
	}

	req := &taskspb.CreateTaskRequest{
		Parent: q.queuePath,
		Task: &taskspb.Task{
			Name:         q.taskName(p.ID),
			ScheduleTime: timestamppb.New(p.TriggerAt),
			MessageType: &taskspb.Task_HttpRequest{
				HttpRequest: httpReq,
			},
		},
	}

	err := q.do(ctx, func() error {
		_, err := q.client.CreateTask(ctx, req)
		return err
	})
	if err != nil {
		if status.Code(err) == codes.AlreadyExists {
			// task name is derived from Posterum.ID; duplicate enqueues are safe to ignore
			return nil
		}
		return fmt.Errorf("cloudtasks: create task %s: %w", p.ID, err)
	}
	return nil
}

func (q *Queue) Cancel(ctx context.Context, id string) error {
	req := &taskspb.DeleteTaskRequest{
		Name: q.taskName(id),
	}
	err := q.do(ctx, func() error {
		return q.client.DeleteTask(ctx, req)
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil
		}
		return fmt.Errorf("cloudtasks: delete task %s: %w", id, err)
	}
	return nil
}

// do runs op, retrying transient gRPC failures per the WithRetry config.
func (q *Queue) do(ctx context.Context, op func() error) error {
	attempts := q.maxAttempts
	if attempts < 1 {
		attempts = 1
	}
	delay := q.baseDelay
	for attempt := 1; ; attempt++ {
		err := op()
		if err == nil {
			return nil
		}
		if attempt >= attempts || !retryable(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
		}
		delay *= 2
	}
}

func retryable(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted:
		return true
	default:
		return false
	}
}

func (q *Queue) taskName(id string) string {
	return fmt.Sprintf("%s/tasks/%s", q.queuePath, id)
}

func (q *Queue) headersFromPosterum(p postera.Posterum) map[string]string {
	var headers map[string]string
	for _, h := range q.headers {
		if v := h.get(p); v != "" {
			if headers == nil {
				headers = make(map[string]string)
			}
			headers[h.headerName] = v
		}
	}
	return headers
}

var _ postera.Queue = (*Queue)(nil)
