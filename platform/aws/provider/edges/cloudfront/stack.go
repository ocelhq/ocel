package cloudfront

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/pkg/providerkit"
	kitledger "github.com/ocelhq/ocel/pkg/providerkit/ledger"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type private struct {
	Distribution        string `json:"distribution,omitempty"`
	StateTable          string `json:"stateTable,omitempty"`
	AssetBucket         string `json:"assetBucket,omitempty"`
	Region              string `json:"region,omitempty"`
	Function            string `json:"function,omitempty"`
	KeyValueStore       string `json:"keyValueStore,omitempty"`
	CachePolicy         string `json:"cachePolicy,omitempty"`
	HeadersPolicy       string `json:"headersPolicy,omitempty"`
	OriginAccessControl string `json:"originAccessControl,omitempty"`
	PreviewBase         string `json:"previewBase,omitempty"`
}

type stack struct {
	p     *provider
	state edge.StackState
	own   private
}

var _ edge.EdgeStack = (*stack)(nil)

func (s *stack) State() edge.StackState {
	held := s.state
	held.Adapter = edge.Own(s.own)
	return held
}

func (s *stack) Ledger() edge.Ledger { return &lazyLedger{s: s} }

func (s *stack) slug() string { return s.state.Slug }

func (s *stack) class() edge.Class { return s.state.Class }

func (s *stack) plan() distributionPlan {
	return distributionPlan{
		name:          distributionName(s.slug(), s.class()),
		assetOrigin:   assetOriginDomain(s.own.AssetBucket, s.own.Region),
		function:      s.own.Function,
		cachePolicy:   s.own.CachePolicy,
		headersPolicy: s.own.HeadersPolicy,
		oac:           s.own.OriginAccessControl,
	}
}

func (s *stack) ledger(c Clients) *kitledger.Ledger {
	return awsports.Ledger(c.Dynamo, awsports.Table(s.own.StateTable), s.class(), s.slug())
}

func (s *stack) routes(c Clients) routeWriter {
	return routeWriter{clients: c, arn: s.own.KeyValueStore}
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

func (s *stack) reconcileDistribution(ctx context.Context, c Clients) (front, error) {
	plan := s.plan()
	held, found, err := s.findDistributionFor(ctx, c, plan.name)
	if err != nil {
		return front{}, err
	}
	if !found {
		created, err := createDistribution(ctx, c, plan, nil, "")
		if err != nil {
			return front{}, err
		}
		held = created
	} else if err := reshapeDistribution(ctx, c, plan, held.id); err != nil {
		return front{}, err
	}
	if err := s.ledger(c).NoteInvalidationTarget(ctx, held.id); err != nil {
		return front{}, err
	}
	s.recordFront(held)
	return held, nil
}

func (s *stack) ensureDistribution(ctx context.Context, c Clients) (front, error) {
	plan := s.plan()
	held, found, err := s.findDistributionFor(ctx, c, plan.name)
	if err != nil {
		return front{}, err
	}
	if !found {
		created, err := createDistribution(ctx, c, plan, nil, "")
		if err != nil {
			return front{}, err
		}
		if err := s.ledger(c).NoteInvalidationTarget(ctx, created.id); err != nil {
			return front{}, err
		}
		held = created
	}
	s.recordFront(held)
	return held, nil
}

func (s *stack) recordFront(held front) {
	s.own.Distribution = held.id
	s.state.Front = held.domainName
}

func (s *stack) findDistributionFor(ctx context.Context, c Clients, name string) (front, bool, error) {
	if s.own.Distribution != "" && s.state.Front != "" {
		return front{id: s.own.Distribution, domainName: s.state.Front}, true, nil
	}
	return findDistribution(ctx, c, name)
}

func (s *stack) Promote(ctx context.Context, promotion edge.Promotion, pointer string, report edge.Reporter) error {
	c, err := s.p.clientsFor(ctx)
	if err != nil {
		return err
	}
	hostnames := s.servedHostnames(pointer)
	if len(hostnames) > 0 {
		published, err := s.routeFor(ctx, c, promotion)
		if err != nil {
			return err
		}
		puts := make(map[string]route, len(hostnames))
		for _, hostname := range hostnames {
			puts[hostname] = published
		}
		if err := s.routes(c).apply(ctx, puts, nil); err != nil {
			return err
		}
	}
	if err := s.ledger(c).Promote(ctx, promotion, pointer, report); err != nil {
		return errors.Join(err, s.unpublishUnrecorded(ctx, c, pointer))
	}
	return nil
}

func (s *stack) unpublishUnrecorded(ctx context.Context, c Clients, pointer string) error {
	host := s.previewHost(pointer)
	if host == "" {
		return nil
	}
	pointers, err := s.ledger(c).Pointers(ctx)
	if err != nil || slices.Contains(pointers, pointerOr(pointer)) {
		return err
	}
	return s.routes(c).apply(ctx, nil, []string{host})
}

func (s *stack) servedHostnames(pointer string) []string {
	if host := s.previewHost(pointer); host != "" {
		return []string{host}
	}
	if pointerOr(pointer) != edge.DefaultPointer {
		return nil
	}
	return s.state.Bound
}

func (s *stack) previewSite() edge.PreviewSite {
	if s.class() != edge.ClassPreview {
		return edge.PreviewSite{}
	}
	base := s.state.GlobalPreview
	if base == "" {
		base = s.own.PreviewBase
	}
	return edge.SharedPreview(s.slug(), base)
}

func (s *stack) onPreviewWildcard() bool {
	return s.own.Distribution == "" && s.previewSite().Serves()
}

func (s *stack) previewHost(pointer string) string {
	if pointerOr(pointer) == edge.DefaultPointer {
		return ""
	}
	return s.previewSite().Host(pointer, "")
}

func (s *stack) unroutePreviews(ctx context.Context, c Clients) error {
	if !s.previewSite().Serves() {
		return nil
	}
	pointers, err := s.ledger(c).Pointers(ctx)
	if err != nil {
		return err
	}
	hosts := make([]string, 0, len(pointers))
	for _, pointer := range pointers {
		if host := s.previewHost(pointer); host != "" {
			hosts = append(hosts, host)
		}
	}
	if len(hosts) == 0 {
		return nil
	}
	return s.routes(c).apply(ctx, nil, hosts)
}

func (s *stack) routeFor(ctx context.Context, c Clients, promotion edge.Promotion) (route, error) {
	apps := slices.Sorted(maps.Keys(promotion.Builds))
	switch {
	case len(apps) == 0:
		return route{}, fmt.Errorf("promote %s: it names no app, and every hostname the %q edge answers on points at one app's entry function; deploy an app before promoting", promotion.PromotionID, Kind)
	case len(apps) > 1:
		return route{}, fmt.Errorf("promote %s: this project deploys %d apps (%s), and the %q edge points a hostname at one release's entry function, so it cannot serve more than one of them. Split the apps into one project each, or give each app its own project domain", promotion.PromotionID, len(apps), strings.Join(apps, ", "), Kind)
	}

	app := apps[0]
	identity := promotion.Builds[app]
	record, found, err := s.ledger(c).Record(ctx, app, identity)
	if err != nil {
		return route{}, err
	}
	if !found {
		return route{}, fmt.Errorf("promote %s: the deployments ledger holds no record for %s/%s, so nothing names the release the edge would point at; re-run the deploy that built it", promotion.PromotionID, app, identity)
	}
	if record.EntryFunction == "" {
		return route{}, fmt.Errorf("promote %s: the deployment record for %s/%s names no entry function, so the edge has nothing to reach. That record was written by an older CLI than the one that serves it; re-run the deploy to write it again", promotion.PromotionID, app, identity)
	}
	origin := originHost(record.FunctionURLs[record.Entry])
	if origin == "" {
		return route{}, fmt.Errorf("promote %s: the deployment record for %s/%s names entry function %s but no URL the edge can reach it on, and the %q edge fronts a release over its entry function's URL; re-run the deploy to write the record again", promotion.PromotionID, app, identity, record.EntryFunction, Kind)
	}
	secret, err := s.originSecret(ctx, c)
	if err != nil {
		return route{}, err
	}
	return route{
		Stack:       s.plan().name,
		Origin:      origin,
		Release:     identity,
		Assets:      assetOriginDomain(s.own.AssetBucket, s.own.Region),
		AssetPrefix: assetOriginPath(record.AssetPrefix),
		Secret:      secret,
	}, nil
}

func (s *stack) originSecret(ctx context.Context, c Clients) (string, error) {
	command := providerkit.BootstrapCommand(s.class())
	name, err := bootstrap.OriginSecretParamFor(string(s.class()))
	if err != nil {
		return "", err
	}
	out, err := c.SSM.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(name),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("read the secret the entry function demands of the front that reaches it: %s is unreadable. Re-run `%s` against this account to mint it: %w", name, command, err)
	}
	secret := aws.ToString(out.Parameter.Value)
	if secret == "" {
		return "", fmt.Errorf("read the secret the entry function demands of the front that reaches it: %s holds nothing. Re-run `%s` against this account to mint it", name, command)
	}
	return secret, nil
}

func (s *stack) RemovePointer(ctx context.Context, pointer string) (edge.PruneResult, error) {
	c, err := s.p.clientsFor(ctx)
	if err != nil {
		return edge.PruneResult{}, err
	}
	if host := s.previewHost(pointer); host != "" {
		if err := s.routes(c).apply(ctx, nil, []string{host}); err != nil {
			return edge.PruneResult{}, err
		}
	}
	return s.ledger(c).RemovePointer(ctx, pointer)
}

func (s *stack) BindDomain(ctx context.Context, binding edge.DomainBinding) error {
	c, err := s.p.clientsFor(ctx)
	if err != nil {
		return err
	}
	held, err := s.ensureDistribution(ctx, c)
	if err != nil {
		return err
	}
	if err := serveAlias(ctx, c, s.plan(), held.id, binding.Hostname, binding.Certificate); err != nil {
		return err
	}
	s.state.Bind(binding.Hostname)
	return nil
}

func (s *stack) UnbindDomain(ctx context.Context, hostname string) error {
	c, err := s.p.clientsFor(ctx)
	if err != nil {
		return err
	}
	if err := s.routes(c).apply(ctx, nil, []string{hostname}); err != nil {
		return err
	}
	if id := s.own.Distribution; id != "" {
		if err := dropAlias(ctx, c, s.plan(), id, hostname); err != nil {
			return err
		}
	}
	s.state.Release(hostname)
	return nil
}

func (s *stack) Destroy(ctx context.Context) error {
	c, err := s.p.clientsFor(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, hostname := range s.state.Bound {
		if err := s.UnbindDomain(ctx, hostname); err != nil {
			errs = append(errs, fmt.Errorf("unbind %q before destroying the stack that serves it: %w", hostname, err))
		}
	}
	unrouted := s.unroutePreviews(ctx, c)
	if unrouted != nil {
		errs = append(errs, fmt.Errorf("stop serving this project's previews before erasing the deployments ledger that names them, so a re-run still knows which hostnames to withdraw: %w", unrouted))
	}
	gone := true
	if !s.onPreviewWildcard() {
		held, found, err := s.findDistributionFor(ctx, c, s.plan().name)
		switch {
		case err != nil:
			errs = append(errs, err)
			gone = false
		case found:
			if err := s.p.deleteDistribution(ctx, c, kindDistribution, held.id); err != nil {
				errs = append(errs, err)
				gone = false
			} else if err := s.ledger(c).ForgetInvalidationTarget(ctx, held.id); err != nil {
				errs = append(errs, err)
			}
		}
	}
	s.own.Distribution, s.state.Front = "", ""
	if unrouted == nil && gone {
		if err := s.ledger(c).Destroy(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
