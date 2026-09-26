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

	kitledger "github.com/ocelhq/ocel/pkg/providerkit/ledger"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type private struct {
	API         string `json:"api,omitempty"`
	StateTable  string `json:"stateTable,omitempty"`
	AssetBucket string `json:"assetBucket,omitempty"`
	Role        string `json:"invokeRole,omitempty"`
	Region      string `json:"region,omitempty"`
}

type stack struct {
	p     *apiGateway
	state edge.StackState
	own   private
}

var _ edge.EdgeStack = (*stack)(nil)

func (s *stack) State() edge.StackState {
	state := s.state
	state.Private = edge.Own(s.own)
	return state
}

func (s *stack) Ledger() edge.Ledger { return &lazyLedger{s: s} }

func (s *stack) slug() string { return s.state.Slug }

func (s *stack) class() edge.Class { return s.state.Class }

func (s *stack) spec(pointer string) apiSpec {
	return apiSpec{
		name:        apiName(s.p.ns, s.slug(), s.class(), pointer),
		region:      s.own.Region,
		account:     accountOf(s.own.Role),
		role:        s.own.Role,
		assetBucket: s.own.AssetBucket,
	}
}

func (s *stack) ledger(c Clients) *kitledger.Ledger {
	return awsports.Ledger(c.Dynamo, awsports.Table(s.own.StateTable), s.class(), s.slug())
}

type lazyLedger struct{ s *stack }

var _ edge.Ledger = (*lazyLedger)(nil)

func (l *lazyLedger) resolve(ctx context.Context) (*kitledger.Ledger, error) {
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
	spec, id, err := s.apiFor(ctx, c, pointer)
	if err != nil {
		return "", err
	}
	if err := shapeAPI(ctx, c, spec, id); err != nil {
		return "", err
	}
	return id, nil
}

func (s *stack) ensureAPI(ctx context.Context, c Clients, pointer string) (string, error) {
	spec, id, err := s.apiFor(ctx, c, pointer)
	if err != nil {
		return "", err
	}
	shaped, err := stagePresent(ctx, c, id)
	if err != nil {
		return "", err
	}
	if !shaped {
		if err := shapeAPI(ctx, c, spec, id); err != nil {
			return "", err
		}
	}
	return id, nil
}

func (s *stack) apiFor(ctx context.Context, c Clients, pointer string) (apiSpec, string, error) {
	spec := s.spec(pointer)
	id, found, err := s.findAPIFor(ctx, c, pointer, spec.name)
	if err != nil {
		return apiSpec{}, "", err
	}
	if found {
		return spec, id, nil
	}
	if spec.role == "" || spec.region == "" {
		return apiSpec{}, "", fmt.Errorf("the stack serving %s records no invoke role or region; reconcile it before promoting into it", s.slug())
	}
	id, err = createAPI(ctx, c, spec)
	if err != nil {
		return apiSpec{}, "", err
	}
	return spec, id, nil
}

func (s *stack) findAPIFor(ctx context.Context, c Clients, pointer, name string) (string, bool, error) {
	if pointerOr(pointer) == edge.DefaultPointer {
		if id := s.own.API; id != "" {
			return id, true, nil
		}
	}
	return findAPI(ctx, c, name)
}

func (s *stack) Promote(ctx context.Context, promotion edge.Promotion, pointer string, progress edge.Progress) error {
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
	if err := moveStage(ctx, c, id, promotion.PromotionID, patch); err != nil {
		return err
	}
	if err := s.routePreview(ctx, c, pointer, id); err != nil {
		return err
	}
	if err := s.ledger(c).Promote(ctx, promotion, pointer, progress); err != nil {
		return errors.Join(err, s.restage(ctx, c, pointer, id))
	}
	return nil
}

func moveStage(ctx context.Context, c Clients, id, promotionID string, patch []agtypes.PatchOperation) error {
	if _, err := c.APIGateway.UpdateStage(ctx, &apigateway.UpdateStageInput{
		RestApiId:       aws.String(id),
		StageName:       aws.String(stageName),
		PatchOperations: patch,
	}); err != nil {
		if promotionID == "" {
			return fmt.Errorf("move the %s stage of REST API %s back off a promotion the ledger refused: %w", stageName, id, err)
		}
		return fmt.Errorf("move the %s stage of REST API %s onto promotion %s: %w", stageName, id, promotionID, err)
	}
	return nil
}

func (s *stack) restage(ctx context.Context, c Clients, pointer, id string) error {
	active, found, err := s.activePromotion(ctx, c, pointer)
	if err != nil {
		return err
	}
	if !found {
		if err := s.unroutePreview(ctx, c, pointer); err != nil {
			return err
		}
		return moveStage(ctx, c, id, "", unsetPatch())
	}
	patch, err := s.stagePatch(ctx, c, active)
	if err != nil {
		return err
	}
	return moveStage(ctx, c, id, active.PromotionID, patch)
}

func (s *stack) activePromotion(ctx context.Context, c Clients, pointer string) (edge.Promotion, bool, error) {
	history, err := s.ledger(c).History(ctx, pointer)
	if err != nil {
		return edge.Promotion{}, false, err
	}
	for _, entry := range history {
		if entry.Active {
			return entry.Promotion, true, nil
		}
	}
	return edge.Promotion{}, false, nil
}

func unsetPatch() []agtypes.PatchOperation {
	return variablePatch(unsetVariables())
}

func variablePatch(variables map[string]string) []agtypes.PatchOperation {
	patch := make([]agtypes.PatchOperation, 0, len(variables))
	for _, name := range slices.Sorted(maps.Keys(variables)) {
		patch = append(patch, agtypes.PatchOperation{
			Op:    agtypes.OpReplace,
			Path:  aws.String("/variables/" + name),
			Value: aws.String(variables[name]),
		})
	}
	return patch
}

func (s *stack) stagePatch(ctx context.Context, c Clients, promotion edge.Promotion) ([]agtypes.PatchOperation, error) {
	apps := slices.Sorted(maps.Keys(promotion.Builds))
	switch {
	case len(apps) == 0:
		return nil, fmt.Errorf("promote %s: it names no app, and the %s stage serves one app's entry function; deploy an app before promoting", promotion.PromotionID, stageName)
	case len(apps) > 1:
		return nil, fmt.Errorf("promote %s: this project deploys %d apps (%s), and the %q edge fronts a project with a single REST API whose %s stage names one entry function, so it cannot serve more than one of them. Split the apps into one project each, or put an edge that routes by hostname in front by naming one in your config, such as `\"edge\": \"cloudflare\"`", promotion.PromotionID, len(apps), strings.Join(apps, ", "), Kind, stageName)
	}

	app := apps[0]
	identity := promotion.Builds[app]
	record, found, err := s.ledger(c).Record(ctx, app, identity)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("promote %s: the deployments ledger has no record for %s/%s, so nothing names the function the %s stage would serve; re-run the deploy that built it", promotion.PromotionID, app, identity, stageName)
	}
	if record.Origin != "" {
		return nil, fmt.Errorf("promote %s: %s/%s runs as a container at %s, and the %q edge invokes a release's entry function rather than reaching a URL, so it cannot front it; name an edge that reaches an origin by URL in your config, such as `\"edge\": \"cloudfront\"`", promotion.PromotionID, app, identity, record.Origin, Kind)
	}
	if record.EntryFunction == "" {
		return nil, fmt.Errorf("promote %s: the deployment record for %s/%s names no entry function, so the %s stage has nothing to invoke. That record was written by an older CLI than the one that serves it; re-run the deploy to write it again", promotion.PromotionID, app, identity, stageName)
	}

	assets := record.AssetPrefix
	if assets == "" {
		assets = unsetVariable
	}
	return variablePatch(map[string]string{
		entryVariable:  record.EntryFunction,
		assetsVariable: assets,
	}), nil
}

func (s *stack) RemovePointer(ctx context.Context, pointer string, _ edge.Progress) (edge.PruneResult, error) {
	c, err := s.p.clientsFor(ctx)
	if err != nil {
		return edge.PruneResult{}, err
	}
	if pointerOr(pointer) != edge.DefaultPointer {
		if err := s.unroutePreview(ctx, c, pointer); err != nil {
			return edge.PruneResult{}, err
		}
		id, found, err := findAPI(ctx, c, apiName(s.p.ns, s.slug(), s.class(), pointer))
		if err != nil {
			return edge.PruneResult{}, err
		}
		if found {
			if err := s.p.deletion().drain(ctx, c, []string{id}); err != nil {
				return edge.PruneResult{}, err
			}
		}
	}
	return s.ledger(c).RemovePointer(ctx, pointer)
}

func (s *stack) previewHost(pointer string) (string, string) {
	base := s.state.GlobalPreview
	if base == "" || s.class() != edge.ClassPreview {
		return "", ""
	}
	if pointerOr(pointer) == edge.DefaultPointer {
		return "", ""
	}
	host := edge.SharedPreview(s.slug(), base).Host(pointer, "")
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
	base := s.state.GlobalPreview
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
	front, err := ensureDomainName(ctx, c, binding)
	if err != nil {
		return err
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
	s.state.Bind(binding.Hostname)
	s.state.PublishFront(binding.Hostname, front)
	return nil
}

func ensureDomainName(ctx context.Context, c Clients, binding edge.DomainBinding) (string, error) {
	current, err := c.APIGateway.GetDomainName(ctx, &apigateway.GetDomainNameInput{
		DomainName: aws.String(binding.Hostname),
	})
	if err == nil {
		return regionalFrontOf(binding.Hostname, aws.ToString(current.RegionalDomainName))
	}
	if !isNotFound(err) {
		return "", fmt.Errorf("read the API Gateway domain name for %s: %w", binding.Hostname, err)
	}
	created, err := c.APIGateway.CreateDomainName(ctx, &apigateway.CreateDomainNameInput{
		DomainName:             aws.String(binding.Hostname),
		RegionalCertificateArn: aws.String(binding.Certificate),
		SecurityPolicy:         agtypes.SecurityPolicyTls12,
		EndpointConfiguration: &agtypes.EndpointConfiguration{
			Types: []agtypes.EndpointType{agtypes.EndpointTypeRegional},
		},
	})
	if err != nil {
		return "", fmt.Errorf("create the API Gateway domain name for %s: %w", binding.Hostname, err)
	}
	return regionalFrontOf(binding.Hostname, aws.ToString(created.RegionalDomainName))
}

func regionalFrontOf(hostname, regional string) (string, error) {
	if regional == "" {
		return "", fmt.Errorf("API Gateway named no regional domain name for %s, so nothing says where its DNS record should point", hostname)
	}
	return regional, nil
}

func (s *stack) publishDomainFronts(ctx context.Context, c Clients, warn func(string)) error {
	for _, hostname := range s.state.Bound {
		if s.state.Fronts[hostname] != "" {
			continue
		}
		current, err := c.APIGateway.GetDomainName(ctx, &apigateway.GetDomainNameInput{
			DomainName: aws.String(hostname),
		})
		if err != nil {
			if isNotFound(err) {
				s.state.Release(hostname)
				s.state.PublishFront(hostname, "")
				if warn != nil {
					warn(fmt.Sprintf("%s is no longer bound: the API Gateway domain name it was served on is gone, so nothing answers it and no record can point at it — run `ocel domain add` to bind it again", hostname))
				}
				continue
			}
			return fmt.Errorf("read the API Gateway domain name for %s: %w", hostname, err)
		}
		front, err := regionalFrontOf(hostname, aws.ToString(current.RegionalDomainName))
		if err != nil {
			return err
		}
		s.state.PublishFront(hostname, front)
	}
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
	s.state.Release(hostname)
	s.state.PublishFront(hostname, "")
	return nil
}

func (s *stack) Destroy(ctx context.Context) error {
	c, err := s.p.clientsFor(ctx)
	if err != nil {
		return err
	}
	var errs []error
	unbound := true
	for _, hostname := range s.state.Bound {
		if err := s.UnbindDomain(ctx, hostname); err != nil {
			errs = append(errs, fmt.Errorf("unbind %q before destroying the stack that serves it: %w", hostname, err))
			unbound = false
		}
	}
	ledger := s.ledger(c)
	pointers, err := ledger.Pointers(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	names := []string{apiName(s.p.ns, s.slug(), s.class(), "")}
	for _, pointer := range pointers {
		names = append(names, apiName(s.p.ns, s.slug(), s.class(), pointer))
	}
	if err := s.unrouteProject(ctx, c); err != nil {
		errs = append(errs, err)
	}
	ids, err := findAPIs(ctx, c, names)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	if s.own.API != "" && !slices.Contains(ids, s.own.API) {
		ids = append(ids, s.own.API)
	}
	drained := s.p.deletion().drain(ctx, c, ids)
	if drained != nil {
		return errors.Join(append(errs, drained)...)
	}
	s.own.API = ""
	if !unbound {
		return errors.Join(errs...)
	}
	if err := ledger.Destroy(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
