package postera

import (
	"context"
	"time"
)

type Posterum struct {
	ID        string
	Message   string
	ExecuteAt time.Time
}

type Registry interface {
	// Get
	Save(ctx context.Context, ownerID string, p Posterum) error
	Remove(ctx context.Context, ownerID, id string) error
	// List
}
