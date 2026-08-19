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

	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
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

func (s *stack) plan() distributionPlan {
	return distributionPlan{
		name:          distributionName(s.slug(), s.class()),
		assetOrigin:   assetOriginDomain(s.state[stackKeyAssetBucket], s.state[stackKeyRegion]),
		function:      s.state[stackKeyFunction],
		cachePolicy:   s.state[stackKeyCachePolicy],
		headersPolicy: s.state[stackKeyHeadersPolicy],
		oac:           s.state[stackKeyOAC],
	}
}

func (s *stack) ledger(c Clients) *edgeledger.Ledger {
	return &edgeledger.Ledger{
		Dynamo: c.Dynamo,
		Table:  s.state[stackKeyStateTable],
		Scope:  edgeledger.Scope(s.class(), s.slug()),
	}
}

func (s *stack) routes(c Clients) routeWriter {
	return routeWriter{clients: c, arn: s.state[stackKeyKeyValueStore]}
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
	if s.state == nil {
		s.state = edge.StackState{}
	}
	s.state[stackKeyDistribution] = held.id
	s.state[edge.StackKeyFront] = held.domainName
}

func (s *stack) findDistributionFor(ctx context.Context, c Clients, name string) (front, bool, error) {
	if id := s.state[stackKeyDistribution]; id != "" {
		if domain := s.state[edge.StackKeyFront]; domain != "" {
			return front{id: id, domainName: domain}, true, nil
		}
	}
	return findDistribution(ctx, c, name)
}

func (s *stack) Promote(ctx context.Context, promotion edge.Promotion, pointer string) error {
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
	if err := s.ledger(c).Promote(ctx, promotion, pointer); err != nil {
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
	return edge.BoundDomains(s.state)
}

func (s *stack) previewBase() string {
	if s.class() != edge.ClassPreview {
		return ""
	}
	if base := s.state[edge.StackKeyGlobalPreview]; base != "" {
		return base
	}
	return s.state[stackKeyPreviewBase]
}

func (s *stack) onPreviewWildcard() bool {
	return s.state[stackKeyDistribution] == "" && s.previewBase() != ""
}

func (s *stack) previewHost(pointer string) string {
	base := s.previewBase()
	if base == "" || pointerOr(pointer) == edge.DefaultPointer {
		return ""
	}
	return edge.PreviewHost(s.slug(), pointer, "", base)
}

func (s *stack) unroutePreviews(ctx context.Context, c Clients) error {
	if s.previewBase() == "" {
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
		Assets:      assetOriginDomain(s.state[stackKeyAssetBucket], s.state[stackKeyRegion]),
		AssetPrefix: assetOriginPath(record.AssetPrefix),
		Secret:      secret,
	}, nil
}

func (s *stack) originSecret(ctx context.Context, c Clients) (string, error) {
	name, err := bootstrap.OriginSecretParamFor(string(s.class()))
	if err != nil {
		return "", err
	}
	out, err := c.SSM.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(name),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("read the secret the entry function demands of the front that reaches it: %s is unreadable. Re-run `ocel bootstrap` against this account to mint it: %w", name, err)
	}
	secret := aws.ToString(out.Parameter.Value)
	if secret == "" {
		return "", fmt.Errorf("read the secret the entry function demands of the front that reaches it: %s holds nothing. Re-run `ocel bootstrap` against this account to mint it", name)
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
	s.state = edge.RecordBoundDomain(s.state, binding.Hostname)
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
	if id := s.state[stackKeyDistribution]; id != "" {
		if err := dropAlias(ctx, c, s.plan(), id, hostname); err != nil {
			return err
		}
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
	delete(s.state, stackKeyDistribution)
	delete(s.state, edge.StackKeyFront)
	if unrouted == nil && gone {
		if err := s.ledger(c).Destroy(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
