package binding

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"

	"connectrpc.com/connect"
	"ocel.dev"
	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

type Resource[C any] struct {
	Client C
}

type Connection[C any] struct {
	mu       sync.Mutex
	resource *Resource[C]
}

type serviceClientConstructor[C any] func(httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) C

func (c *Connection[C]) Connect(resource, access string, dial func() (*Resource[C], error)) (*Resource[C], error) {
	if Discovering() {
		return nil, &ocel.UnprovisionedError{Resource: resource, Access: access}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resource != nil {
		return c.resource, nil
	}
	dialed, err := dial()
	if err != nil {
		return nil, err
	}
	c.resource = dialed
	return dialed, nil
}

func Dial[C any](name string, bindingType bindingsv1.BindingType, newClient serviceClientConstructor[C]) (*Resource[C], error) {
	if err := RefuseUnbound(name, bindingType); err != nil {
		return nil, err
	}
	address, authorized, err := ReadRuntimeConnection()
	if err != nil {
		return nil, err
	}
	return &Resource[C]{Client: newClient(http.DefaultClient, address, authorized)}, nil
}

func ReadRuntimeConnection() (string, connect.ClientOption, error) {
	address := os.Getenv(RuntimeAddressEnv)
	if address == "" {
		return "", nil, &missingRuntimeAddressError{}
	}
	token := os.Getenv(SessionTokenEnv)
	if token == "" {
		return "", nil, &missingSessionTokenError{}
	}
	return address, connect.WithInterceptors(newBearerInterceptor(token)), nil
}

type missingRuntimeAddressError struct{}

func (*missingRuntimeAddressError) Error() string {
	return fmt.Sprintf(
		"%s is not defined, so no resource the ocel runtime serves can be reached. "+
			"Run `ocel dev` to serve it locally, or `ocel deploy` to have the deployed runtime's address delivered.",
		RuntimeAddressEnv,
	)
}

type missingSessionTokenError struct{}

func (*missingSessionTokenError) Error() string {
	return fmt.Sprintf(
		"%s is not defined, so the ocel runtime at %s would refuse every call. "+
			"It is delivered beside %s by `ocel dev` and by the deployed runtime, never set by hand.",
		SessionTokenEnv, RuntimeAddressEnv, RuntimeAddressEnv,
	)
}

func newBearerInterceptor(token string) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", Bearer(token))
			return next(ctx, req)
		}
	})
}
