package apigateway

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigateway"
	agtypes "github.com/aws/aws-sdk-go-v2/service/apigateway/types"

	"github.com/ocelhq/ocel/platform/aws/provider/edgeledger"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type stack struct {
	p     *provider
	state edge.StackState
}

var _ edge.EdgeStack = (*stack)(nil)

func (s *stack) State() edge.StackState { return s.state }

func (s *stack) Ledger() edge.Ledger { return &lazyLedger{s: s} }

func (s *stack) slug() string { return s.state[edge.StackKeySlug] }

func (s *stack) class() edge.Class { return edge.Class(s.state[edge.StackKeyClass]) }

func (s *stack) plan(pointer string) apiPlan {
	return apiPlan{
		name:        apiName(s.slug(), s.class(), pointer),
		region:      s.state[stackKeyRegion],
		account:     accountOf(s.state[stackKeyRole]),
		role:        s.state[stackKeyRole],
		assetBucket: s.state[stackKeyAssetBucket],
	}
}

func (s *stack) ledger(c Clients) *edgeledger.Ledger {
	return &edgeledger.Ledger{
		Dynamo: c.Dynamo,
		Table:  s.state[stackKeyStateTable],
		Scope:  string(s.class()) + "/" + s.slug(),
	}
}

type lazyLedger struct{ s *stack }

var _ edge.Ledger = (*lazyLedger)(nil)

func (l *lazyLedger) resolve(ctx context.Context) (*edgeledger.Ledger, error) {
	c, err := l.s.p.clientsFor(ctx)
	if err != nil {
		return nil, err
	}
	return l.s.ledger(c), nil
}

func (l *lazyLedger) SchemaVersion(ctx context.Context) (int, error) {
	ledger, err := l.resolve(ctx)
	if err != nil {
		return 0, err
	}
	return ledger.SchemaVersion(ctx)
}

func (l *lazyLedger) PutStaged(ctx context.Context, record edge.DeploymentRecord) error {
	ledger, err := l.resolve(ctx)
	if err != nil {
		return err
	}
	return ledger.PutStaged(ctx, record)
}

func (l *lazyLedger) History(ctx context.Context, pointer string) ([]edge.HistoryEntry, error) {
	ledger, err := l.resolve(ctx)
	if err != nil {
		return nil, err
	}
	return ledger.History(ctx, pointer)
}

func (l *lazyLedger) Prune(ctx context.Context, keepN int, pointer string) (edge.PruneResult, error) {
	ledger, err := l.resolve(ctx)
	if err != nil {
		return edge.PruneResult{}, err
	}
	return ledger.Prune(ctx, keepN, pointer)
}

func (s *stack) reconcileAPI(ctx context.Context, c Clients, pointer string) (string, error) {
	plan, id, err := s.apiFor(ctx, c, pointer)
	if err != nil {
		return "", err
	}
	if err := shapeAPI(ctx, c, plan, id); err != nil {
		return "", err
	}
	return id, nil
}

func (s *stack) ensureAPI(ctx context.Context, c Clients, pointer string) (string, error) {
	plan, id, err := s.apiFor(ctx, c, pointer)
	if err != nil {
		return "", err
	}
	shaped, err := stagePresent(ctx, c, id)
	if err != nil {
		return "", err
	}
	if !shaped {
		if err := shapeAPI(ctx, c, plan, id); err != nil {
			return "", err
		}
	}
	return id, nil
}

func (s *stack) apiFor(ctx context.Context, c Clients, pointer string) (apiPlan, string, error) {
	plan := s.plan(pointer)
	id, found, err := s.findAPIFor(ctx, c, pointer, plan.name)
	if err != nil {
		return apiPlan{}, "", err
	}
	if found {
		return plan, id, nil
	}
	if plan.role == "" || plan.region == "" {
		return apiPlan{}, "", fmt.Errorf("the stack serving %s carries no invoke role or region; reconcile it before promoting into it", s.slug())
	}
	id, err = createAPI(ctx, c, plan)
	if err != nil {
		return apiPlan{}, "", err
	}
	return plan, id, nil
}

func (s *stack) findAPIFor(ctx context.Context, c Clients, pointer, name string) (string, bool, error) {
	if pointerOr(pointer) == edgeledger.DefaultPointer {
		if id := s.state[stackKeyAPI]; id != "" {
			return id, true, nil
		}
	}
	return findAPI(ctx, c, name)
}

func (s *stack) Promote(ctx context.Context, promotion edge.Promotion, pointer string) error {
	c, err := s.p.clientsFor(ctx)
	if err != nil {
		return err
	}
	id, err := s.ensureAPI(ctx, c, pointer)
	if err != nil {
		return err
	}
	patch, err := s.stagePatch(ctx, c, promotion)
	if err != nil {
		return err
	}
	if _, err := c.APIGateway.UpdateStage(ctx, &apigateway.UpdateStageInput{
		RestApiId:       aws.String(id),
		StageName:       aws.String(stageName),
		PatchOperations: patch,
	}); err != nil {
		return fmt.Errorf("move the %s stage of REST API %s onto promotion %s: %w", stageName, id, promotion.PromotionID, err)
	}
	if err := s.routePreview(ctx, c, pointer, id); err != nil {
		return err
	}
	return s.ledger(c).Promote(ctx, promotion, pointer)
}

func (s *stack) stagePatch(ctx context.Context, c Clients, promotion edge.Promotion) ([]agtypes.PatchOperation, error) {
	apps := slices.Sorted(maps.Keys(promotion.Builds))
	switch {
	case len(apps) == 0:
		return nil, fmt.Errorf("promote %s: it names no app, and the %s stage serves one app's entry function; deploy an app before promoting", promotion.PromotionID, stageName)
	case len(apps) > 1:
		return nil, fmt.Errorf("promote %s: this project deploys %d apps (%s), and the none edge fronts a project with a single REST API whose %s stage names one entry function, so it cannot serve more than one of them. Split the apps into one project each, or put an edge that routes by hostname in front by deploying with `--edge cloudflare`", promotion.PromotionID, len(apps), strings.Join(apps, ", "), stageName)
	}

	app := apps[0]
	identity := promotion.Builds[app]
	record, found, err := s.ledger(c).Record(ctx, app, identity)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("promote %s: the deployments ledger holds no record for %s/%s, so nothing names the function the %s stage would serve; re-run the deploy that built it", promotion.PromotionID, app, identity, stageName)
	}
	if record.EntryFunction == "" {
		return nil, fmt.Errorf("promote %s: the deployment record for %s/%s names no entry function, so the %s stage has nothing to invoke. That record was written by an older CLI than the one that serves it; re-run the deploy to write it again", promotion.PromotionID, app, identity, stageName)
	}

	assets := record.AssetPrefix
	if assets == "" {
		assets = unsetVariable
	}
	return []agtypes.PatchOperation{
		{
			Op:    agtypes.OpReplace,
			Path:  aws.String("/variables/" + entryVariable),
			Value: aws.String(record.EntryFunction),
		},
		{
			Op:    agtypes.OpReplace,
			Path:  aws.String("/variables/" + assetsVariable),
			Value: aws.String(assets),
		},
	}, nil
}

func (s *stack) RemovePointer(ctx context.Context, pointer string) (edge.PruneResult, error) {
	c, err := s.p.clientsFor(ctx)
	if err != nil {
		return edge.PruneResult{}, err
	}
	if pointerOr(pointer) != edgeledger.DefaultPointer {
		if err := s.unroutePreview(ctx, c, pointer); err != nil {
			return edge.PruneResult{}, err
		}
		id, found, err := findAPI(ctx, c, apiName(s.slug(), s.class(), pointer))
		if err != nil {
			return edge.PruneResult{}, err
		}
		if found {
			if err := deleteAPI(ctx, c, id); err != nil {
				return edge.PruneResult{}, err
			}
		}
	}
	return s.ledger(c).RemovePointer(ctx, pointer)
}

func (s *stack) previewHost(pointer string) (string, string) {
	base := s.state[edge.StackKeyGlobalPreview]
	if base == "" || s.class() != edge.ClassPreview {
		return "", ""
	}
	if pointerOr(pointer) == edgeledger.DefaultPointer {
		return "", ""
	}
	host := edge.PreviewHost(s.slug(), pointer, "", base)
	if host == "" {
		return "", ""
	}
	return edge.PreviewWildcard(base), host
}

func (s *stack) routePreview(ctx context.Context, c Clients, pointer, api string) error {
	wildcard, host := s.previewHost(pointer)
	if host == "" {
		return nil
	}
	return putHostRule(ctx, c, wildcard, host, api, 0)
}

func (s *stack) unroutePreview(ctx context.Context, c Clients, pointer string) error {
	wildcard, host := s.previewHost(pointer)
	if host == "" {
		return nil
	}
	return deleteHostRule(ctx, c, wildcard, host)
}

func (s *stack) unrouteProject(ctx context.Context, c Clients) error {
	base := s.state[edge.StackKeyGlobalPreview]
	if base == "" || s.class() != edge.ClassPreview || s.slug() == "" {
		return nil
	}
	return deleteLabelledRules(ctx, c, edge.PreviewWildcard(base), s.slug()+edge.PreviewAppSeparator, "."+base)
}

func (s *stack) BindDomain(ctx context.Context, binding edge.DomainBinding) error {
	c, err := s.p.clientsFor(ctx)
	if err != nil {
		return err
	}
	id, err := s.ensureAPI(ctx, c, "")
	if err != nil {
		return err
	}
	if _, err := c.APIGateway.GetDomainName(ctx, &apigateway.GetDomainNameInput{
		DomainName: aws.String(binding.Hostname),
	}); err != nil {
		if !isNotFound(err) {
			return fmt.Errorf("read the API Gateway domain name for %s: %w", binding.Hostname, err)
		}
		if _, err := c.APIGateway.CreateDomainName(ctx, &apigateway.CreateDomainNameInput{
			DomainName:             aws.String(binding.Hostname),
			RegionalCertificateArn: aws.String(binding.Certificate),
			SecurityPolicy:         agtypes.SecurityPolicyTls12,
			EndpointConfiguration: &agtypes.EndpointConfiguration{
				Types: []agtypes.EndpointType{agtypes.EndpointTypeRegional},
			},
		}); err != nil {
			return fmt.Errorf("create the API Gateway domain name for %s: %w", binding.Hostname, err)
		}
	}
	mappings, err := basePathMappings(ctx, c, binding.Hostname)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(mappings, func(m agtypes.BasePathMapping) bool {
		return aws.ToString(m.RestApiId) == id
	}) {
		if _, err := c.APIGateway.CreateBasePathMapping(ctx, &apigateway.CreateBasePathMappingInput{
			DomainName: aws.String(binding.Hostname),
			RestApiId:  aws.String(id),
			Stage:      aws.String(stageName),
		}); err != nil {
			return fmt.Errorf("map %s onto REST API %s: %w", binding.Hostname, id, err)
		}
	}
	s.state = edge.RecordBoundDomain(s.state, binding.Hostname)
	return nil
}

func (s *stack) UnbindDomain(ctx context.Context, hostname string) error {
	c, err := s.p.clientsFor(ctx)
	if err != nil {
		return err
	}
	mappings, err := basePathMappings(ctx, c, hostname)
	if err != nil {
		return err
	}
	for _, mapping := range mappings {
		base := aws.ToString(mapping.BasePath)
		if base == "" {
			base = "(none)"
		}
		if _, err := c.APIGateway.DeleteBasePathMapping(ctx, &apigateway.DeleteBasePathMappingInput{
			DomainName: aws.String(hostname),
			BasePath:   aws.String(base),
		}); err != nil && !isNotFound(err) {
			return fmt.Errorf("unmap %s: %w", hostname, err)
		}
	}
	if _, err := c.APIGateway.DeleteDomainName(ctx, &apigateway.DeleteDomainNameInput{
		DomainName: aws.String(hostname),
	}); err != nil && !isNotFound(err) {
		return fmt.Errorf("delete the API Gateway domain name for %s: %w", hostname, err)
	}
	s.state = edge.ForgetBoundDomain(s.state, hostname)
	return nil
}

func (s *stack) Destroy(ctx context.Context) error {
	c, err := s.p.clientsFor(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, hostname := range edge.BoundDomains(s.state) {
		if err := s.UnbindDomain(ctx, hostname); err != nil {
			errs = append(errs, fmt.Errorf("unbind %q before destroying the stack that serves it: %w", hostname, err))
		}
	}
	ledger := s.ledger(c)
	pointers, err := ledger.Pointers(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	names := []string{apiName(s.slug(), s.class(), "")}
	for _, pointer := range pointers {
		names = append(names, apiName(s.slug(), s.class(), pointer))
	}
	if err := s.unrouteProject(ctx, c); err != nil {
		errs = append(errs, err)
	}
	ids, err := findAPIs(ctx, c, names)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, id := range ids {
		if err := deleteAPI(ctx, c, id); err != nil {
			errs = append(errs, err)
		}
	}
	delete(s.state, stackKeyAPI)
	if err := ledger.Destroy(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
