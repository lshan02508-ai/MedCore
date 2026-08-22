package proxy

import (
	"context"
	"sync/atomic"
)

type proxyOutcome struct{ failed atomic.Bool }
type proxyOutcomeKey struct{}

func withProxyOutcome(ctx context.Context, outcome *proxyOutcome) context.Context {
	return context.WithValue(ctx, proxyOutcomeKey{}, outcome)
}
func markProxyOutcome(ctx context.Context, failed bool) {
	if outcome, ok := ctx.Value(proxyOutcomeKey{}).(*proxyOutcome); ok && failed {
		outcome.failed.Store(true)
	}
}
