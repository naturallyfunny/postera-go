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

type Config struct {
	ProjectID           string
	LocationID          string
	QueueID             string
	TargetURL           string
	ServiceAccountEmail string
}

type Enqueuer struct {
	client              *gcptasks.Client
	queuePath           string
	targetURL           string
	serviceAccountEmail string
	headerMappings      []headerMapping
}

type headerMapping struct {
	ctxKey     any
	headerName string
}

type Option func(*Enqueuer)

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

func NewEnqueuer(ctx context.Context, cfg Config, opts ...Option) (*Enqueuer, error) {
	if cfg.ProjectID == "" || cfg.LocationID == "" || cfg.QueueID == "" {
		return nil, errors.New("cloudtasks: ProjectID, LocationID, and QueueID must be non-empty")
	}
	if cfg.TargetURL == "" {
		return nil, errors.New("cloudtasks: TargetURL must be non-empty")
	}

	client, err := gcptasks.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("cloudtasks: new client: %w", err)
	}

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

func (e *Enqueuer) Close() error {
	return e.client.Close()
}

func (e *Enqueuer) Enqueue(ctx context.Context, p postera.Posterum) error {
	httpReq := &taskspb.HttpRequest{
		Url:        e.targetURL,
		HttpMethod: taskspb.HttpMethod_POST,
		Body:       []byte(p.Message),
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
