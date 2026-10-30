package providerserver

import (
	"context"
	"fmt"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/scope"
)

type servedProject struct {
	alone bool
	slug  string
	tier  environment.Tier
}

func (p servedProject) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if err := p.refuseOther(req.Any()); err != nil {
			return nil, err
		}
		return next(ctx, req)
	}
}

func (p servedProject) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (p servedProject) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	if !p.alone && p.tier == "" {
		return next
	}
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return next(ctx, servedProjectConn{StreamingHandlerConn: conn, served: p})
	}
}

type servedProjectConn struct {
	connect.StreamingHandlerConn
	served servedProject
}

func (c servedProjectConn) Receive(msg any) error {
	if err := c.StreamingHandlerConn.Receive(msg); err != nil {
		return err
	}
	return c.served.refuseOther(msg)
}

func (p servedProject) refuseOther(msg any) error {
	message, ok := msg.(proto.Message)
	if !ok {
		return nil
	}
	if err := p.refuseOtherTier(message); err != nil {
		return err
	}
	if !p.alone {
		return nil
	}
	for _, slug := range scope.ListProjectSlugs(message) {
		switch {
		case p.slug == "":
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this session serves no project, and the request names %s: open the session for %s", slug, slug))
		case slug != p.slug:
			return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("this session serves %s alone, and the request names %s", p.slug, slug))
		}
	}
	return nil
}

func (p servedProject) refuseOtherTier(message proto.Message) error {
	if p.tier == "" {
		return nil
	}
	for _, named := range scope.ListProjectTiers(message) {
		if tier, err := decodeTier(named); err == nil && tier != p.tier {
			return errOtherTier(p.tier, tier)
		}
	}
	return nil
}

func errOtherTier(served, asked environment.Tier) error {
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("this session serves the %s tier alone, and the request acts on %s: send it for %s", served, asked, asked))
}
