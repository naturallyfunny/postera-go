package postera

import "context"

type Enqueuer interface {
	Enqueue(ctx context.Context, p Posterum) error
	Cancel(ctx context.Context, id string) error
}
