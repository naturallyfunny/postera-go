package postera

import (
	"context"
	"errors"
)

// ErrScheduleOutOfRange reports that a posterum's TriggerAt falls outside the
// window a Queue implementation can schedule — either too far in the future
// (beyond the infrastructure's scheduling horizon) or too close to now (racy /
// effectively immediate). Consumers can match it with errors.Is and map it to a
// client error (e.g. HTTP 400) instead of an infrastructure failure (5xx).
var ErrScheduleOutOfRange = errors.New("posterum schedule out of range")

// Queue represents the queue infrastructure where posterums are scheduled and
// canceled (e.g. Google Cloud Tasks).
type Queue interface {
	Enqueue(ctx context.Context, p Posterum) error
	Cancel(ctx context.Context, id string) error
}
