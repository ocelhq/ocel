package endpoint

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"
	"connectrpc.com/validate"

	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
)

func VariablesMount(source variablestoreserver.BackendSource) Mount {
	path, handler := variablestorev1connect.NewVariableStoreServiceHandler(
		&variablestoreserver.Service{Source: source},
		connect.WithInterceptors(validate.NewInterceptor(), refuseUnregisteredSync()),
	)
	return Mount{Path: path, Handler: handler}
}

func refuseUnregisteredSync() connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
			if synced, ok := request.Any().(*variablestorev1.SyncEnvSourceRequest); ok && synced.GetRegistered() == nil {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New(
					"an endpoint syncs only the env source a deploy registered, never one it names: sync the registered one, or change the tier's envSource and deploy"))
			}
			return next(ctx, request)
		}
	})
}
