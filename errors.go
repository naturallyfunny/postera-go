package postera

import "errors"

// ErrNotFound is returned by a Registry — and propagated by Postarius — when
// no Posterum exists for a given id.
var ErrNotFound = errors.New("postera: posterum not found")

// ErrInvalidInput is returned by Postarius when a caller-supplied value
// fails validation at the public API boundary (for example, a zero
// ExecuteAt or a negative day count). It lets callers distinguish a
// programming error in their own code from an infrastructure failure
// surfaced by a Registry or an Enqueuer using errors.Is.
var ErrInvalidInput = errors.New("postera: invalid input")
