package postera

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("posterum not found")

type Query struct {
	Human   string
	Agent   string
	Session string
	From    time.Time
	To      time.Time
}

type Registry interface {
	Save(ctx context.Context, p Posterum) error
	Get(ctx context.Context, id string) (Posterum, error)
	Remove(ctx context.Context, id string) error
	List(ctx context.Context, q Query) ([]Posterum, error)
}
