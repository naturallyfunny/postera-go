package postera

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("posterum not found")

type TimeRange struct {
	From time.Time
	To   time.Time
}

type Registry interface {
	Save(ctx context.Context, p Posterum) error
	Get(ctx context.Context, id string) (Posterum, error)
	Remove(ctx context.Context, id string) error
	List(ctx context.Context, q TimeRange) ([]Posterum, error)
}
