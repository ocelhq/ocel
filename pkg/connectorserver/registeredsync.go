package connectorserver

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"

	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
)

func refuseUnregisteredSync() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
			if synced, ok := request.Any().(*variablestorev1.SyncEnvSourceRequest); ok && synced.GetRegistered() == nil {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New(
					"a connector syncs only the env source a deploy registered, never one it names: sync the registered one, or change the tier's envSource and deploy"))
			}
			return next(ctx, request)
		}
	})
}
