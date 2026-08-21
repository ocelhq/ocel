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

func teardownPlanItems(class string, edgeKind edge.Kind, deployed bootstrap.Deployed, sharedPassphrase bool) ([]*contractv1.RemovalItem, error) {
	stackName, err := bootstrap.StackNameFor(class)
	if err != nil {
		return nil, err
	}
	userName, err := bootstrap.EdgeUserNameFor(class)
	if err != nil {
		return nil, err
	}
	params, err := bootstrap.ClassParamNames(class)
	if err != nil {
		return nil, err
	}

	items := []*contractv1.RemovalItem{{
		Kind:   "edge bootstrap",
		Name:   string(edgeKind),
		Action: contractv1.RemovalItem_ACTION_DELETE,
		Reason: fmt.Sprintf("every worker, cache store and credential the %s edge stood up for the %s bootstrap", edgeKind, class),
		Slow:   true,
	}}

	if deployed.Present {
		for _, bucket := range []struct{ name, reason string }{
			{deployed.StateBucket, "the Pulumi state of every stack this bootstrap deployed, all versions of it; nothing can describe or remove those resources afterwards"},
			{deployed.ArtifactBucket, "the function code staged for this bootstrap"},
			{deployed.AssetBucket, "every build's static assets, prerender fallbacks and edge fetch cache"},
		} {
			if bucket.name == "" {
				continue
			}
			items = append(items, bucketItem(bucket.name, bucket.reason))
		}
		if deployed.StateTable != "" {
			items = append(items, &contractv1.RemovalItem{
				Kind:   "state table",
				Name:   deployed.StateTable,
				Action: contractv1.RemovalItem_ACTION_DELETE,
				Reason: "the stack index teardown walks and the ISR tag clock the edge reads",
			})
		}
		if deployed.VarsTable != "" {
			items = append(items, &contractv1.RemovalItem{
				Kind:   "variable store",
				Name:   deployed.VarsTable,
				Action: contractv1.RemovalItem_ACTION_DELETE,
				Reason: fmt.Sprintf("every %s variable value in this account, and the key they are encrypted under", class),
			})
		}
		if deployed.Features.Has(bootstrap.FeatureCloudflareEdge) {
			items = append(items, &contractv1.RemovalItem{
				Kind:   "edge reader",
				Name:   userName,
				Action: contractv1.RemovalItem_ACTION_DELETE,
				Reason: "the IAM user the edge signs its calls into this account with, and its access key",
			})
		}
		order, err := bootstrap.FeatureDeleteOrder(deployed.Features.Names())
		if err != nil {
			return nil, err
		}
		for _, feature := range order {
			items = append(items, &contractv1.RemovalItem{
				Kind:   "feature stack",
				Name:   bootstrap.FeatureStackName(feature, class),
				Action: contractv1.RemovalItem_ACTION_DELETE,
				Reason: fmt.Sprintf("the CloudFormation stack carrying the %s feature of this bootstrap", feature),
			})
		}
		items = append(items, &contractv1.RemovalItem{
			Kind:   "bootstrap stack",
			Name:   stackName,
			Action: contractv1.RemovalItem_ACTION_DELETE,
			Reason: "the CloudFormation stack holding the core every feature above was built on",
		})
	}

	for _, name := range params {
		items = append(items, &contractv1.RemovalItem{
			Kind:   "parameter",
			Name:   name,
			Action: contractv1.RemovalItem_ACTION_DELETE,
			Reason: "a handle this bootstrap stored; nothing reads it once the bootstrap is gone",
		})
	}

	passphrase := &contractv1.RemovalItem{
		Kind:   "parameter",
		Name:   bootstrap.PassphraseParamName,
		Action: contractv1.RemovalItem_ACTION_DELETE,
		Reason: "the only copy of the passphrase every Pulumi stack in this account is encrypted under",
	}
	if sharedPassphrase {
		sibling, err := bootstrap.SiblingClassOf(class)
		if err != nil {
			return nil, err
		}
		passphrase.Action = contractv1.RemovalItem_ACTION_KEEP
		passphrase.Reason = fmt.Sprintf("the %s bootstrap still stands and its Pulumi state is encrypted under it", sibling)
	}
	return append(items, passphrase), nil
}

func bucketItem(name, reason string) *contractv1.RemovalItem {
	return &contractv1.RemovalItem{
		Kind:   "bucket",
		Name:   name,
		Action: contractv1.RemovalItem_ACTION_DELETE,
		Reason: reason + "; emptied object by object first",
		Slow:   true,
	}
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
	deps, err := newTeardownDeps(ctx, opts, class, edgeFront)
	if err != nil {
		return nil, err
	}
	return planTeardown(ctx, deps, class)
}

func planTeardown(ctx context.Context, deps teardownDeps, class string) (*contractv1.RemovalPlan, error) {
	occupancy, err := readBootstrapOccupancy(ctx, deps, class)
	if err != nil {
		return nil, err
	}
	if err := occupancy.refuse(class); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	sharedPassphrase, err := bootstrap.PassphraseHeldBySibling(ctx, deps.cfn, class)
	if err != nil {
		return nil, err
	}
	items, err := teardownPlanItems(class, deps.edge.Kind(), deps.deployed, sharedPassphrase)
	if err != nil {
		return nil, err
	}
	return &contractv1.RemovalPlan{EdgeKind: string(deps.edge.Kind()), Items: items, Subject: class}, nil
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

	deps, err := newTeardownDeps(ctx, opts, class, edgeFront)
	if err != nil {
		return failStream(stream, err)
	}
	if err := runTeardown(ctx, deps, class, progress, logf); err != nil {
		return failStream(stream, err)
	}
	s.memo.forgetDeployed()
	return stream.Send(okResult())
}

func runTeardown(ctx context.Context, deps teardownDeps, class string, progress, logf func(string)) error {
	occupancy, err := readBootstrapOccupancy(ctx, deps, class)
	if err != nil {
		return err
	}
	if err := occupancy.refuse(class); err != nil {
		return err
	}

	progress(fmt.Sprintf("Tearing down the %s edge", deps.edge.Kind()))
	if err := deps.edge.Teardown(ctx, edge.Class(class)); err != nil {
		return fmt.Errorf("tear down %s edge: %w", deps.edge.Kind(), err)
	}

	return bootstrap.Teardown(ctx, bootstrap.TeardownAPIs{
		CFN:     deps.cfn,
		SSM:     deps.ssm,
		IAM:     deps.iam,
		Buckets: deps.buckets,
	}, class, progress, logf)
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
