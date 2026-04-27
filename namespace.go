package postera

import "context"

// namespaceKey is the unexported, zero-sized type used as the context key
// for the namespace value. Type identity alone — not a string field —
// guarantees that no key in any other package can collide with this one,
// and the empty struct carries no data and incurs no allocation.
type namespaceKey struct{}

// NamespaceKey is the context key under which postera stores the agent's
// namespace. It is exported so that external Registry and Enqueuer
// implementations can read from the same context — for example, to map the
// namespace onto a database column or an HTTP header — without depending on
// the helpers in this package.
var NamespaceKey = namespaceKey{}

// WithNamespace returns a derived context that carries namespace.
//
// An empty namespace is rejected: it is indistinguishable from "no
// namespace set" at the extraction site, so accepting it would silently
// defeat the partition guarantee that namespaces exist to provide. The
// caller passing "" is a programming error and WithNamespace panics so
// that the bug surfaces at its source rather than corrupting downstream
// reads.
func WithNamespace(ctx context.Context, namespace string) context.Context {
	if namespace == "" {
		panic("postera: WithNamespace called with empty namespace")
	}
	return context.WithValue(ctx, NamespaceKey, namespace)
}

// NamespaceFromContext returns the namespace stored in ctx and a boolean
// that reports whether one was present.
//
// postera is identity-agnostic and does not itself require a namespace.
// Registry and Enqueuer implementations that mandate one must enforce that
// requirement in their own layer; the comma-ok signature here exists so
// that single-tenant implementations can legitimately observe absence
// without it being modeled as an error.
func NamespaceFromContext(ctx context.Context) (string, bool) {
	namespace, ok := ctx.Value(NamespaceKey).(string)
	return namespace, ok
}
