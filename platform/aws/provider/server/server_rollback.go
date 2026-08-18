package server

import (
	"context"
	"errors"
	"time"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/service/ssm"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var errNoProductionDeploy = errors.New("this project has no production deploys yet; run `ocel deploy` first")

func (s *Server) openStack(ctx context.Context, opts options, slug string) (edge.EdgeStack, error) {
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return nil, err
	}
	state, err := bootstrap.ReadStackState(ctx, ssm.NewFromConfig(awscfg), slug)
	if err != nil {
		return nil, err
	}
	return s.openStackFor(state, awscfg.Region)
}

func (s *Server) openStackFor(state edge.StackState, region string) (edge.EdgeStack, error) {
	edgeFront, err := s.originEdge(region)
	if err != nil {
		return nil, err
	}
	return openStackOn(edgeFront, state)
}

func openStackOn(edgeFront edge.Edge, state edge.StackState) (edge.EdgeStack, error) {
	if len(state) == 0 {
		return nil, errNoProductionDeploy
	}
	return edgeFront.Open(state)
}

func (s *Server) ListPromotions(ctx context.Context, req *deploymentsv1.ListPromotionsRequest) (*deploymentsv1.ListPromotionsResponse, error) {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	stack, err := s.openStack(ctx, opts, req.GetSlug())
	if err != nil {
		if errors.Is(err, errNoProductionDeploy) {
			return &deploymentsv1.ListPromotionsResponse{}, nil
		}
		return nil, err
	}

	history, err := stack.Ledger().History(ctx, "")
	if err != nil {
		return nil, err
	}
	return &deploymentsv1.ListPromotionsResponse{Promotions: toPromotionHistory(history)}, nil
}

func (s *Server) Rollback(ctx context.Context, req *deploymentsv1.RollbackRequest) (*deploymentsv1.RollbackResponse, error) {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	edgeFront, err := s.originEdge(opts.Region)
	if err != nil {
		return nil, err
	}
	stack, err := s.openStack(ctx, opts, req.GetSlug())
	if err != nil {
		return nil, err
	}

	promoted, err := deploy.Rollback(ctx, edgeFront, stack, req.GetTo(), req.GetTag(), time.Now().Unix())
	if err != nil {
		return nil, err
	}
	return &deploymentsv1.RollbackResponse{Promoted: toPromotionProto(promoted)}, nil
}

func toPromotionHistory(history []edge.HistoryEntry) []*deploymentsv1.PromotionHistoryEntry {
	out := make([]*deploymentsv1.PromotionHistoryEntry, 0, len(history))
	for _, h := range history {
		out = append(out, &deploymentsv1.PromotionHistoryEntry{
			Promotion: toPromotionProto(h.Promotion),
			Active:    h.Active,
		})
	}
	return out
}

func toPromotionProto(p edge.Promotion) *deploymentsv1.Promotion {
	return &deploymentsv1.Promotion{
		PromotionId: p.PromotionID,
		Ts:          p.Ts,
		Builds:      p.Builds,
		Tag:         p.Tag,
		FlipBound:   toFlipBoundProto(p.Flip),
	}
}

func toFlipBoundProto(flip *edge.FlipBound) *deploymentsv1.FlipBound {
	if flip == nil {
		return nil
	}
	return &deploymentsv1.FlipBound{
		TypicalMs: flip.Typical.Milliseconds(),
		Published: flip.Published,
	}
}
