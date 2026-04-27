package postera

import "time"

// Query is a half-open time-range filter used by Registry.List.
//
// The lower bound is inclusive; the upper bound is exclusive. A zero From
// disables the lower bound; a zero To disables the upper bound.
type Query struct {
	From time.Time
	To   time.Time
}
