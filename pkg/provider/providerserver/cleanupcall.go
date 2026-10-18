package providerserver

import "context"

func newCleanupCallContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), shutdownWindow)
}
