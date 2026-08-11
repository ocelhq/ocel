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

func (s *Server) rootStack(ctx context.Context, opts options, slug string) (edge.RootStack, edge.RootStackState, error) {
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return nil, nil, err
	}
	state, err := bootstrap.ReadRootStackState(ctx, ssm.NewFromConfig(awscfg), slug)
	if err != nil {
		return nil, nil, err
	}
	return s.rootStackFor(state)
}

func (s *Server) rootStackFor(state edge.RootStackState) (edge.RootStack, edge.RootStackState, error) {
	if len(state) == 0 {
		return nil, nil, errNoProductionDeploy
	}
	stack, ok := s.edge().(edge.RootStack)
	if !ok {
		return nil, nil, errors.New("this edge does not support the root stack (instant rollback)")
	}
	return stack, state, nil
}

func (s *Server) ListPromotions(ctx context.Context, req *deploymentsv1.ListPromotionsRequest) (*deploymentsv1.ListPromotionsResponse, error) {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	stack, state, err := s.rootStack(ctx, opts, req.GetSlug())
	if err != nil {
		if errors.Is(err, errNoProductionDeploy) {
			return &deploymentsv1.ListPromotionsResponse{}, nil
		}
		return nil, err
	}

	history, err := stack.History(ctx, state, "")
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
	stack, state, err := s.rootStack(ctx, opts, req.GetSlug())
	if err != nil {
		return nil, err
	}

	promoted, err := deploy.Rollback(ctx, stack, state, req.GetTo(), req.GetTag(), time.Now().Unix())
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
	}
}
