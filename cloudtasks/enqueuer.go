package cloudtasks

import (
	"context"
	"errors"
	"fmt"

	cloudtaskspkg "cloud.google.com/go/cloudtasks/apiv2"
	taskspb "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"go.naturallyfunny.dev/postera"
)

type headerMapping struct {
	headerName string
	get        func(postera.Posterum) string
}

type Enqueuer struct {
	client              *cloudtaskspkg.Client
	queuePath           string
	targetURL           string
	serviceAccountEmail string
	headers             []headerMapping
}

type Option func(*Enqueuer) error

func WithTargetURL(url string) Option {
	return func(e *Enqueuer) error {
		if url == "" {
			return errors.New("cloudtasks: WithTargetURL: empty url")
		}
		e.targetURL = url
		return nil
	}
}

func WithServiceAccountEmail(email string) Option {
	return func(e *Enqueuer) error {
		if email == "" {
			return errors.New("cloudtasks: WithServiceAccountEmail: empty email")
		}
		e.serviceAccountEmail = email
		return nil
	}
}

func WithHumanHeader(headerName string) Option {
	return func(e *Enqueuer) error {
		if headerName == "" {
			return errors.New("cloudtasks: WithHumanHeader: empty headerName")
		}
		e.headers = append(e.headers, headerMapping{
			headerName: headerName,
			get:        func(p postera.Posterum) string { return p.Human },
		})
		return nil
	}
}

func WithAgentHeader(headerName string) Option {
	return func(e *Enqueuer) error {
		if headerName == "" {
			return errors.New("cloudtasks: WithAgentHeader: empty headerName")
		}
		e.headers = append(e.headers, headerMapping{
			headerName: headerName,
			get:        func(p postera.Posterum) string { return p.Agent },
		})
		return nil
	}
}

func WithSessionHeader(headerName string) Option {
	return func(e *Enqueuer) error {
		if headerName == "" {
			return errors.New("cloudtasks: WithSessionHeader: empty headerName")
		}
		e.headers = append(e.headers, headerMapping{
			headerName: headerName,
			get:        func(p postera.Posterum) string { return p.Session },
		})
		return nil
	}
}

func WithMetadataHeader(key, headerName string) Option {
	return func(e *Enqueuer) error {
		if key == "" {
			return errors.New("cloudtasks: WithMetadataHeader: empty key")
		}
		if headerName == "" {
			return errors.New("cloudtasks: WithMetadataHeader: empty headerName")
		}
		e.headers = append(e.headers, headerMapping{
			headerName: headerName,
			get:        func(p postera.Posterum) string { return p.Metadata[key] },
		})
		return nil
	}
}

func WithFixedHeader(name, value string) Option {
	return func(e *Enqueuer) error {
		if name == "" {
			return errors.New("cloudtasks: WithFixedHeader: empty name")
		}
		if value == "" {
			return errors.New("cloudtasks: WithFixedHeader: empty value")
		}
		e.headers = append(e.headers, headerMapping{
			headerName: name,
			get:        func(_ postera.Posterum) string { return value },
		})
		return nil
	}
}

func NewEnqueuer(client *cloudtaskspkg.Client, project, location, queue string, opts ...Option) (*Enqueuer, error) {
	if project == "" || location == "" || queue == "" {
		return nil, errors.New("cloudtasks: project, location, and queue must be non-empty")
	}

	e := &Enqueuer{
		client:    client,
		queuePath: fmt.Sprintf("projects/%s/locations/%s/queues/%s", project, location, queue),
	}
	for _, opt := range opts {
		if err := opt(e); err != nil {
			return nil, err
		}
	}
	return e, nil
}

func (e *Enqueuer) Enqueue(ctx context.Context, p postera.Posterum) error {
	if e.targetURL == "" {
		return errors.New("cloudtasks: enqueue: no target URL configured; use WithTargetURL")
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
			// task name is derived from Posterum.ID; duplicate enqueues are safe to ignore
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
	var headers map[string]string
	for _, h := range e.headers {
		if v := h.get(p); v != "" {
			if headers == nil {
				headers = make(map[string]string)
			}
			headers[h.headerName] = v
		}
	}
	return headers
}

var _ postera.Enqueuer = (*Enqueuer)(nil)
