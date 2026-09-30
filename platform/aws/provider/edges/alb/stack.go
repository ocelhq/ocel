package alb

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbv2 "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbv2types "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
)

const certificateQuota = "Certificates per Application Load Balancer (L-9365A611)"

type hostRule struct {
	App                  string `json:"app"`
	Pointer              string `json:"pointer"`
	Rule                 string `json:"rule"`
	Certificate          string `json:"certificate,omitempty"`
	CertificateRequested bool   `json:"certificateRequested,omitempty"`
}

type servedGroups map[string]map[string]string

type recorded struct {
	Hosts  map[string]hostRule `json:"hosts,omitempty"`
	Served servedGroups        `json:"served,omitempty"`
}

type stack struct {
	open     func(context.Context, environment.Tier) (Clients, error)
	pause    func(context.Context, time.Duration) error
	state    edge.StackState
	recorded recorded
}

type publicListener struct {
	address string
	arn     string
	mutual  *elbv2types.MutualAuthenticationAttributes
}

func (s *stack) State() router.StackState {
	current := s.state
	current.Private = edge.Own(s.recorded)
	return router.NewStackState(current)
}

func (s *stack) openClients(ctx context.Context) (Clients, error) {
	return s.open(ctx, s.state.Tier)
}

func (s *stack) readPublicListener(ctx context.Context, c Clients) (publicListener, error) {
	name := awsports.PublicBalancerName(s.state.Tier)
	balancers, err := c.Balancers.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{Names: []string{name}})
	var absent *elbv2types.LoadBalancerNotFoundException
	switch {
	case errors.As(err, &absent) || (err == nil && len(balancers.LoadBalancers) == 0):
		return publicListener{}, refusal.Refuse(refusal.CodeNotReady,
			"no container app behind Cloudflare has deployed to the %s tier yet, so no load balancer answers its hostnames: deploy one and its hostnames attach", s.state.Tier)
	case err != nil:
		return publicListener{}, fmt.Errorf("read the load balancer %s: %w", name, err)
	}
	balancer := balancers.LoadBalancers[0]
	listeners, err := c.Balancers.DescribeListeners(ctx, &elbv2.DescribeListenersInput{LoadBalancerArn: balancer.LoadBalancerArn})
	if err != nil {
		return publicListener{}, fmt.Errorf("read the listeners of %s: %w", name, err)
	}
	for _, listener := range listeners.Listeners {
		if aws.ToInt32(listener.Port) == awsports.PublicListenerPort {
			return publicListener{address: aws.ToString(balancer.DNSName), arn: aws.ToString(listener.ListenerArn), mutual: listener.MutualAuthentication}, nil
		}
	}
	return publicListener{}, fmt.Errorf("the load balancer %s has no listener on port %d", name, awsports.PublicListenerPort)
}

func (s *stack) Claim(ctx context.Context, claim router.Claim) (edge.Origin, error) {
	c, err := s.openClients(ctx)
	if err != nil {
		return edge.Origin{}, err
	}
	listener, err := s.readPublicListener(ctx, c)
	if err != nil {
		return edge.Origin{}, err
	}
	if len(claim.ClientCAs) > 0 {
		if err := s.trustClientCAs(ctx, c, claim.Hostname, claim.ClientCAs); err != nil {
			return edge.Origin{}, err
		}
	}
	if claim.Certificate != "" {
		if err := attachCertificate(ctx, c, listener.arn, claim.Hostname, claim.Certificate); err != nil {
			return edge.Origin{}, err
		}
	}
	pointer := router.ResolvePointer(claim.Pointer)
	held, found := s.recorded.Hosts[claim.Hostname]
	routed, err := s.hasRule(ctx, c, held.Rule)
	if err != nil {
		return edge.Origin{}, err
	}
	switch {
	case !found || !routed:
		rule, err := s.placeRule(ctx, c, listener.arn, claim.Hostname, s.readServedActions(pointer, claim.App))
		if err != nil {
			return edge.Origin{}, err
		}
		held.Rule = rule
	case held.App != claim.App || held.Pointer != pointer:
		if _, err := c.Balancers.ModifyRule(ctx, &elbv2.ModifyRuleInput{RuleArn: aws.String(held.Rule), Actions: s.readServedActions(pointer, claim.App)}); err != nil {
			return edge.Origin{}, fmt.Errorf("forward %s to what %s serves on %s: %w", claim.Hostname, claim.App, pointer, err)
		}
	}
	superseded := held
	held.App, held.Pointer, held.Certificate, held.CertificateRequested = claim.App, pointer, claim.Certificate, claim.CertificateRequested
	s.recordHost(claim.Hostname, held)
	if superseded.Certificate != "" && superseded.Certificate != claim.Certificate {
		if err := s.releaseCertificate(ctx, c, listener.arn, claim.Hostname, superseded); err != nil {
			return edge.Origin{}, err
		}
	}
	return edge.Origin{Address: listener.address, Certified: true}, nil
}

func attachCertificate(ctx context.Context, c Clients, listener, hostname, certificate string) error {
	_, err := c.Balancers.AddListenerCertificates(ctx, &elbv2.AddListenerCertificatesInput{
		ListenerArn:  aws.String(listener),
		Certificates: []elbv2types.Certificate{{CertificateArn: aws.String(certificate)}},
	})
	var full *elbv2types.TooManyCertificatesException
	if errors.As(err, &full) {
		return refusal.Refuse(refusal.CodeInvalid,
			"the load balancer every container app behind Cloudflare in this tier shares holds as many certificates as its quota allows, so %s cannot be answered with %s: raise the quota %s in Service Quotas (Elastic Load Balancing), or remove a hostname or preview behind it, and deploy again",
			hostname, certificate, certificateQuota)
	}
	if err != nil {
		return fmt.Errorf("answer %s with certificate %s: %w", hostname, certificate, err)
	}
	return nil
}

func (s *stack) recordHost(hostname string, held hostRule) {
	if s.recorded.Hosts == nil {
		s.recorded.Hosts = map[string]hostRule{}
	}
	s.recorded.Hosts[hostname] = held
}

func (s *stack) readServedActions(pointer, app string) []elbv2types.Action {
	if group := s.recorded.Served[pointer][app]; group != "" {
		return buildForwardActions(group)
	}
	return buildUnservedActions()
}

func (s *stack) Disclaim(ctx context.Context, hostname string) error {
	held, found := s.recorded.Hosts[hostname]
	if !found {
		return nil
	}
	c, err := s.openClients(ctx)
	if err != nil {
		return err
	}
	return s.dropHost(ctx, c, hostname, held)
}

func (s *stack) dropHost(ctx context.Context, c Clients, hostname string, held hostRule) error {
	if err := s.deleteRule(ctx, c, held.Rule); err != nil {
		return err
	}
	if err := s.untrustHostname(ctx, c, hostname); err != nil {
		return err
	}
	if held.Certificate != "" {
		listener, err := s.readPublicListener(ctx, c)
		var refused refusal.Refusal
		switch {
		case errors.As(err, &refused) && refused.Code == refusal.CodeNotReady:
		case err != nil:
			return err
		default:
			if err := s.releaseCertificate(ctx, c, listener.arn, hostname, held); err != nil {
				return err
			}
		}
	}
	delete(s.recorded.Hosts, hostname)
	return nil
}

func (s *stack) releaseCertificate(ctx context.Context, c Clients, listener, hostname string, released hostRule) error {
	if !released.CertificateRequested {
		return nil
	}
	for other, held := range s.recorded.Hosts {
		if other != hostname && held.Certificate == released.Certificate {
			return nil
		}
	}
	if _, err := c.Balancers.RemoveListenerCertificates(ctx, &elbv2.RemoveListenerCertificatesInput{
		ListenerArn:  aws.String(listener),
		Certificates: []elbv2types.Certificate{{CertificateArn: aws.String(released.Certificate)}},
	}); err != nil {
		return fmt.Errorf("stop answering with certificate %s: %w", released.Certificate, err)
	}
	return nil
}

func (s *stack) MovePointer(ctx context.Context, move router.PointerMove, _ progress.Log) error {
	c, err := s.openClients(ctx)
	if err != nil {
		return router.Unserved{Err: err}
	}
	groups, err := readTargetGroups(ctx, c, s.state.Tier, move.Records)
	if err != nil {
		return router.Unserved{Err: err}
	}
	pointer := router.ResolvePointer(move.Pointer)
	moving := s.newLease(c, formatPointerLeaseKey(s.state.Tier, s.state.Slug, pointer), "moving "+pointer+" of "+s.state.Slug)
	worked := false
	err = moving.hold(ctx, func(ctx context.Context) error {
		worked = true
		if err := move.RefuseInactive(ctx); err != nil {
			return err
		}
		for _, app := range slices.Sorted(maps.Keys(groups)) {
			for _, hostname := range s.listHosts(pointer, app) {
				if err := s.forwardRuleTo(ctx, c, s.recorded.Hosts[hostname].Rule, groups[app], move.RefuseInactive); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil && !worked {
		return router.Unserved{Err: err}
	}
	if err != nil {
		return err
	}
	s.recorded.Served = s.recorded.Served.withPointer(pointer, groups)
	return nil
}

func (g servedGroups) withPointer(pointer string, groups map[string]string) servedGroups {
	if g == nil {
		g = servedGroups{}
	}
	if g[pointer] == nil {
		g[pointer] = map[string]string{}
	}
	maps.Copy(g[pointer], groups)
	return g
}

func (s *stack) listHosts(pointer, app string) []string {
	var hosts []string
	for _, hostname := range slices.Sorted(maps.Keys(s.recorded.Hosts)) {
		held := s.recorded.Hosts[hostname]
		if held.Pointer == pointer && (app == "" || held.App == app) {
			hosts = append(hosts, hostname)
		}
	}
	return hosts
}

func (s *stack) RemovePointer(ctx context.Context, removal router.PointerRemoval, _ progress.Log) error {
	resolved := router.ResolvePointer(removal.Pointer)
	hosts := s.listHosts(resolved, "")
	delete(s.recorded.Served, resolved)
	if len(hosts) == 0 {
		return nil
	}
	c, err := s.openClients(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, hostname := range hosts {
		if err := s.dropHost(ctx, c, hostname, s.recorded.Hosts[hostname]); err != nil {
			errs = append(errs, fmt.Errorf("stop routing %s: %w", hostname, err))
		}
	}
	return errors.Join(errs...)
}

func (s *stack) Destroy(ctx context.Context) error {
	var errs []error
	for _, pointer := range s.listPointers() {
		errs = append(errs, s.RemovePointer(ctx, router.PointerRemoval{Pointer: pointer}, progress.Discard()))
	}
	return errors.Join(errs...)
}

func (s *stack) listPointers() []string {
	var pointers []string
	for _, held := range s.recorded.Hosts {
		if !slices.Contains(pointers, held.Pointer) {
			pointers = append(pointers, held.Pointer)
		}
	}
	for pointer := range s.recorded.Served {
		if !slices.Contains(pointers, pointer) {
			pointers = append(pointers, pointer)
		}
	}
	slices.Sort(pointers)
	return pointers
}
