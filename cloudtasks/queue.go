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

type Queue struct {
	client              *cloudtaskspkg.Client
	queuePath           string
	targetURL           string
	serviceAccountEmail string
	headers             []headerMapping
}

type Option func(*Queue) error

func WithTargetURL(url string) Option {
	return func(q *Queue) error {
		if url == "" {
			return errors.New("cloudtasks: WithTargetURL: empty url")
		}
		q.targetURL = url
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

	if _, err := q.client.CreateTask(ctx, req); err != nil {
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
	if err := q.client.DeleteTask(ctx, req); err != nil {
		if status.Code(err) == codes.NotFound {
			return nil
		}
		return fmt.Errorf("cloudtasks: delete task %s: %w", id, err)
	}
	return nil
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
