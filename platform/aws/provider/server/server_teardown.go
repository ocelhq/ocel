package server

import (
	"context"
	"fmt"
	"slices"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/stackindex"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type bootstrapSSMAPI interface {
	bootstrap.SSMAPI
	bootstrap.SSMPathAPI
}

type indexedProjectsAPI interface {
	Projects(ctx context.Context) ([]string, error)
}

type teardownDeps struct {
	edge     edge.Edge
	cfn      bootstrap.CFNTeardownAPI
	ssm      bootstrapSSMAPI
	iam      bootstrap.IAMKeyAPI
	buckets  bootstrap.BucketEmptierAPI
	deployed bootstrap.Deployed
	index    indexedProjectsAPI
}

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

type bootstrapOccupancy struct {
	projects []string
	wildcard string
}

func readBootstrapOccupancy(ctx context.Context, deps teardownDeps, class string) (bootstrapOccupancy, error) {
	edgeStacks, err := bootstrap.StackSlugsFor(ctx, deps.ssm, class)
	if err != nil {
		return bootstrapOccupancy{}, err
	}
	var indexed []string
	if deps.index != nil {
		indexed, err = deps.index.Projects(ctx)
		if err != nil {
			return bootstrapOccupancy{}, err
		}
	}
	occupancy := bootstrapOccupancy{projects: mergeSlugs(edgeStacks, indexed)}
	if class != bootstrap.ClassPreview {
		return occupancy, nil
	}
	recorded, err := bootstrap.ReadPreviewDomain(ctx, deps.ssm, class)
	if err != nil {
		return bootstrapOccupancy{}, err
	}
	occupancy.wildcard = recorded.BaseDomain
	return occupancy, nil
}

func mergeSlugs(lists ...[]string) []string {
	var all []string
	for _, list := range lists {
		all = append(all, list...)
	}
	slices.Sort(all)
	return slices.Compact(all)
}

func (o bootstrapOccupancy) refuse(class string) error {
	if len(o.projects) == 0 && o.wildcard == "" {
		return nil
	}
	var reasons []string
	if len(o.projects) > 0 {
		destroy := "ocel destroy"
		if class == bootstrap.ClassPreview {
			destroy = "ocel destroy --preview"
		}
		reasons = append(reasons, fmt.Sprintf(
			"%d project(s) are still deployed into it: %s — run `%s` in each one first",
			len(o.projects), strings.Join(o.projects, ", "), destroy,
		))
	}
	if o.wildcard != "" {
		reasons = append(reasons, fmt.Sprintf(
			"previews are still served on %s — release it with `ocel domain release --preview` first",
			edge.PreviewWildcard(o.wildcard),
		))
	}
	return fmt.Errorf("the %s bootstrap is still in use, so `%s` will not remove it: %s",
		class, teardownCommand(class), strings.Join(reasons, "; "))
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
	if err := s.stillOccupied(ctx, opts, class, edgeFront); err != nil {
		return nil, providerkit.RefusalError(err)
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

func (s *Server) stillOccupied(ctx context.Context, opts providerConfig, class string, edgeFront edge.Edge) error {
	deps, err := newTeardownDeps(ctx, opts, class, edgeFront)
	if err != nil {
		return err
	}
	occupancy, err := readBootstrapOccupancy(ctx, deps, class)
	if err != nil {
		return err
	}
	return occupancy.refuse(class)
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

	if err := s.stillOccupied(ctx, opts, class, edgeFront); err != nil {
		return failStream(stream, providerkit.RefusalError(err))
	}
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

func newTeardownDeps(ctx context.Context, opts providerConfig, class string, edgeFront edge.Edge) (teardownDeps, error) {
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return teardownDeps{}, err
	}
	cfn := cloudformation.NewFromConfig(awscfg)
	deployed, err := checkBootstrap(ctx, cfn, class == bootstrap.ClassPreview)
	if err != nil {
		return teardownDeps{}, err
	}
	deps := teardownDeps{
		edge:     edgeFront,
		cfn:      cfn,
		ssm:      ssm.NewFromConfig(awscfg),
		iam:      iam.NewFromConfig(awscfg),
		buckets:  s3.NewFromConfig(awscfg),
		deployed: deployed,
	}
	if deployed.StateTable != "" {
		deps.index = &stackindex.Index{Dynamo: dynamodb.NewFromConfig(awscfg), Table: deployed.StateTable}
	}
	return deps, nil
}
