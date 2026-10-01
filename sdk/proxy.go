package ocel

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"

	"connectrpc.com/connect"
	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

type boundResource[C any] struct {
	client C
	name   string
}

type boundResourceConnection[C any] struct {
	once     sync.Once
	resource *boundResource[C]
	err      error
}

type serviceClientConstructor[C any] func(httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) C

func (c *boundResourceConnection[C]) connect(resource, access string, dial func() (*boundResource[C], error)) (*boundResource[C], error) {
	if discovering() {
		return nil, &UnprovisionedError{Resource: resource, Access: access}
	}
	c.once.Do(func() {
		c.resource, c.err = dial()
	})
	return c.resource, c.err
}

func dialBoundResource[C any](name string, bindingType bindingsv1.BindingType, readBoundName func(*bindingsv1.Binding) string, newClient serviceClientConstructor[C]) (*boundResource[C], error) {
	delivered, err := binding(name, bindingType)
	if err != nil {
		return nil, err
	}
	address, authorized, err := readRuntimeConnection()
	if err != nil {
		return nil, err
	}
	return &boundResource[C]{client: newClient(http.DefaultClient, address, authorized), name: readBoundName(delivered)}, nil
}

func readRuntimeConnection() (string, connect.ClientOption, error) {
	address := os.Getenv(runtimeAddressEnv)
	if address == "" {
		return "", nil, &missingRuntimeAddressError{}
	}
	token := os.Getenv(sessionTokenEnv)
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
		runtimeAddressEnv,
	)
}

type missingSessionTokenError struct{}

func (*missingSessionTokenError) Error() string {
	return fmt.Sprintf(
		"%s is not defined, so the ocel runtime at %s would refuse every call. "+
			"It is delivered beside %s by `ocel dev` and by the deployed runtime, never set by hand.",
		sessionTokenEnv, runtimeAddressEnv, runtimeAddressEnv,
	)
}

func newBearerInterceptor(token string) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", bearer(token))
			return next(ctx, req)
		}
	})
}
