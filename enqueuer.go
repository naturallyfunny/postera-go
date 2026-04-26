package postera

import (
	"context"
	"time"
)

type Job struct {
	URL     string
	Method  string
	Headers map[string]string
	Payload []byte
	RunAt   time.Time
}

type Enqueuer interface {
	Enqueue(ctx context.Context, id string, job Job) error
	Cancel(ctx context.Context, id string) error
}
