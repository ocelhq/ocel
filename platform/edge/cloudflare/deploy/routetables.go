package cloudflare

import (
	"context"
	"net/http"
	"net/url"

	"github.com/ocelhq/ocel/pkg/router"
)

func routeTablePath(key string) string { return "/route-table?key=" + url.QueryEscape(key) }

func (r Router) storeRouteTable(ctx context.Context, state router.StackState, key string, table []byte) error {
	_, err := r.p.sendStoreRequest(ctx, state.Edge.Endpoint, state.Edge.Slug, state.Edge.Secret, http.MethodPut, routeTablePath(key), table, nil)
	return err
}

func (r Router) forgetRouteTable(ctx context.Context, state router.StackState, key string) error {
	_, err := r.p.sendStoreRequest(ctx, state.Edge.Endpoint, state.Edge.Slug, state.Edge.Secret, http.MethodDelete, routeTablePath(key), nil, nil)
	return err
}
