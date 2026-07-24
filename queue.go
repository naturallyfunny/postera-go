package postera

import "context"

// Queue represents the queue infrastructure where posterums are scheduled and
// canceled (e.g. Google Cloud Tasks).
type Queue interface {
	Enqueue(ctx context.Context, p Posterum) error
	Cancel(ctx context.Context, id string) error
}
