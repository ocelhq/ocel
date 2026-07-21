package server

import (
	"context"
	"errors"
	"time"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
	"github.com/ocelhq/ocel/cloud/aws/deploy"
	"github.com/ocelhq/ocel/cloud/edge"
	"github.com/ocelhq/ocel/cloud/edge/cloudflare"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// errNoProductionDeploy is returned by ListPromotions/Rollback when a project
// has never had a successful production deploy: rollback and promotion
// history exist only once a root tier has been reconciled (ADR 0001).
var errNoProductionDeploy = errors.New("this project has no production deploys yet; run `ocel deploy` first")

// rootTier resolves the reconciled edge.RootTier state a project's production
// deploys have persisted, erroring clearly when none exists yet. Shared by
// ListPromotions and Rollback, both of which only ever read/act on an
// already-reconciled root tier — neither ever reconciles one itself.
func (s *Server) rootTier(ctx context.Context, opts options, projectID string) (edge.RootTier, edge.RootTierState, error) {
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return nil, nil, err
	}
	ssmClient := ssm.NewFromConfig(awscfg)

	state, err := bootstrap.ReadRootTierState(ctx, ssmClient, projectID)
	if err != nil {
		return nil, nil, err
	}
	if len(state) == 0 {
		return nil, nil, errNoProductionDeploy
	}

	tier, ok := cloudflare.New().(edge.RootTier)
	if !ok {
		return nil, nil, errors.New("this edge does not support the root tier (instant rollback)")
	}
	return tier, state, nil
}

// ListPromotions enumerates a production project's promotion history via its
// already-reconciled root tier. It backs `ocel deployments ls`.
func (s *Server) ListPromotions(ctx context.Context, req *deploymentsv1.ListPromotionsRequest) (*deploymentsv1.ListPromotionsResponse, error) {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	tier, state, err := s.rootTier(ctx, opts, req.GetProjectId())
	if err != nil {
		if errors.Is(err, errNoProductionDeploy) {
			return &deploymentsv1.ListPromotionsResponse{}, nil
		}
		return nil, err
	}

	history, err := tier.History(ctx, state)
	if err != nil {
		return nil, err
	}
	return &deploymentsv1.ListPromotionsResponse{Promotions: toPromotionHistory(history)}, nil
}

// Rollback re-points a production project's active-deployment pointer at a
// prior Promotion: the immediately previous one when req.To is empty, else
// the one named by req.To. It backs `ocel rollback` / `ocel rollback --to
// <promotionId>`.
func (s *Server) Rollback(ctx context.Context, req *deploymentsv1.RollbackRequest) (*deploymentsv1.RollbackResponse, error) {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	tier, state, err := s.rootTier(ctx, opts, req.GetProjectId())
	if err != nil {
		return nil, err
	}

	promoted, err := deploy.Rollback(ctx, tier, state, req.GetTo(), time.Now().Unix())
	if err != nil {
		return nil, err
	}
	return &deploymentsv1.RollbackResponse{Promoted: toPromotionProto(promoted)}, nil
}

// toPromotionHistory maps the store's promotion history to the proto reply.
// Pure.
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

// toPromotionProto maps edge.Promotion to the proto message. Pure.
func toPromotionProto(p edge.Promotion) *deploymentsv1.Promotion {
	return &deploymentsv1.Promotion{
		PromotionId: p.PromotionID,
		Ts:          p.Ts,
		Builds:      p.Builds,
	}
}
