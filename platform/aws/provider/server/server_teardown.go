package server

import (
	"context"
	"fmt"
	"strings"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

func classOf(tier environmentv1.Tier) (string, error) {
	switch tier {
	case environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_UNSPECIFIED:
		return bootstrap.ClassProduction, nil
	case environmentv1.Tier_TIER_PREVIEW:
		return bootstrap.ClassPreview, nil
	default:
		return "", fmt.Errorf("there is no %s bootstrap to tear down; a bootstrap is either production or preview", strings.ToLower(strings.TrimPrefix(tier.String(), "TIER_")))
	}
}

func teardownCommand(class string) string {
	if class == bootstrap.ClassPreview {
		return "ocel bootstrap --destroy --preview"
	}
	return "ocel bootstrap --destroy"
}

func (s *Server) PlanRemoveBootstrap(ctx context.Context, req *contractv1.BootstrapScope) (*contractv1.RemovalPlan, error) {
	opts := s.config.get()
	edgeFront, err := s.edge(requestedEdge(req), opts.Region)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	class, err := classOf(req.GetTier())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	gate, err := s.gated(ctx, class, edgeFront)
	if err != nil {
		return nil, err
	}
	if err := gate.Vacant(ctx, providerkit.Class(class)); err != nil {
		return nil, providerkit.RefusalError(err)
	}
	surfaces, err := gate.bootstrapper.Removals(ctx, providerkit.Class(class))
	if err != nil {
		return nil, providerkit.RefusalError(err)
	}
	return &contractv1.RemovalPlan{
		EdgeKind: string(edgeFront.Kind()),
		Items:    providerkit.RemovalItems(surfaces),
		Subject:  class,
	}, nil
}

func (s *Server) RemoveBootstrap(ctx context.Context, req *contractv1.BootstrapScope, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	opts := s.config.get()
	edgeFront, err := s.edge(requestedEdge(req), opts.Region)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}

	class, err := classOf(req.GetTier())
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}

	progress := func(m string) { _ = stream.Send(progressEvent(m)) }
	logf := func(m string) { _ = stream.Send(logEvent(m)) }

	gate, err := s.gated(ctx, class, edgeFront)
	if err != nil {
		return failStream(stream, err)
	}
	removed := s.applying(func() error {
		return gate.Remove(ctx, providerkit.Class(class), reportTo{say: progress, detail: logf})
	})
	if removed != nil {
		return failStream(stream, providerkit.RefusalError(removed))
	}
	return stream.Send(okResult())
}
