package alb

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

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

type hostRule struct {
	App         string `json:"app"`
	Pointer     string `json:"pointer"`
	Rule        string `json:"rule"`
	Certificate string `json:"certificate,omitempty"`
}

type recorded struct {
	Hosts  map[string]hostRule          `json:"hosts,omitempty"`
	Served map[string]map[string]string `json:"served,omitempty"`
}

type stack struct {
	open     func(context.Context, environment.Tier) (Clients, error)
	state    edge.StackState
	recorded recorded
}

type front struct {
	address  string
	listener string
	mutual   *elbv2types.MutualAuthenticationAttributes
}

func (s *stack) State() router.StackState {
	current := s.state
	current.Private = edge.Own(s.recorded)
	return router.NewStackState(current)
}

func (s *stack) clients(ctx context.Context) (Clients, error) {
	return s.open(ctx, s.state.Tier)
}

func (s *stack) readFront(ctx context.Context, c Clients) (front, error) {
	name := awsports.PublicBalancerName(s.state.Tier)
	balancers, err := c.Balancers.DescribeLoadBalancers(ctx, &elbv2.DescribeLoadBalancersInput{Names: []string{name}})
	var absent *elbv2types.LoadBalancerNotFoundException
	switch {
	case errors.As(err, &absent) || (err == nil && len(balancers.LoadBalancers) == 0):
		return front{}, refusal.Refuse(refusal.CodeNotReady,
			"no container app behind Cloudflare has deployed to the %s tier yet, so no load balancer answers its hostnames: deploy one and its hostnames attach", s.state.Tier)
	case err != nil:
		return front{}, fmt.Errorf("read the load balancer %s: %w", name, err)
	}
	balancer := balancers.LoadBalancers[0]
	listeners, err := c.Balancers.DescribeListeners(ctx, &elbv2.DescribeListenersInput{LoadBalancerArn: balancer.LoadBalancerArn})
	if err != nil {
		return front{}, fmt.Errorf("read the listeners of %s: %w", name, err)
	}
	for _, listener := range listeners.Listeners {
		if aws.ToInt32(listener.Port) == awsports.PublicListenerPort {
			return front{address: aws.ToString(balancer.DNSName), listener: aws.ToString(listener.ListenerArn), mutual: listener.MutualAuthentication}, nil
		}
	}
	return front{}, fmt.Errorf("the load balancer %s has no listener on port %d", name, awsports.PublicListenerPort)
}

func (s *stack) Claim(ctx context.Context, claim router.Claim) (edge.Origin, error) {
	c, err := s.clients(ctx)
	if err != nil {
		return edge.Origin{}, err
	}
	answering, err := s.readFront(ctx, c)
	if err != nil {
		return edge.Origin{}, err
	}
	if len(claim.ClientCertificates) > 0 {
		if err := s.trustClientCertificates(ctx, c, answering, claim.ClientCertificates); err != nil {
			return edge.Origin{}, err
		}
	}
	if claim.Certificate != "" {
		if _, err := c.Balancers.AddListenerCertificates(ctx, &elbv2.AddListenerCertificatesInput{
			ListenerArn:  aws.String(answering.listener),
			Certificates: []elbv2types.Certificate{{CertificateArn: aws.String(claim.Certificate)}},
		}); err != nil {
			return edge.Origin{}, fmt.Errorf("answer %s with certificate %s: %w", claim.Hostname, claim.Certificate, err)
		}
	}
	pointer := router.ResolvePointer(claim.Pointer)
	held, found := s.recorded.Hosts[claim.Hostname]
	if !found || !s.ruleExists(ctx, c, held.Rule) {
		rule, err := s.placeRule(ctx, c, answering.listener, claim.Hostname, s.actionFor(pointer, claim.App))
		if err != nil {
			return edge.Origin{}, err
		}
		held.Rule = rule
	}
	superseded := held.Certificate
	held.App, held.Pointer, held.Certificate = claim.App, pointer, claim.Certificate
	s.recordHost(claim.Hostname, held)
	if superseded != "" && superseded != claim.Certificate {
		if err := s.releaseCertificate(ctx, c, answering.listener, superseded); err != nil {
			return edge.Origin{}, err
		}
	}
	return edge.Origin{Address: answering.address, Certified: true}, nil
}

func (s *stack) recordHost(hostname string, held hostRule) {
	if s.recorded.Hosts == nil {
		s.recorded.Hosts = map[string]hostRule{}
	}
	s.recorded.Hosts[hostname] = held
}

func (s *stack) actionFor(pointer, app string) []elbv2types.Action {
	if group := s.recorded.Served[pointer][app]; group != "" {
		return forwardTo(group)
	}
	return unserved()
}

func (s *stack) Disclaim(ctx context.Context, hostname string) error {
	held, found := s.recorded.Hosts[hostname]
	if !found {
		return nil
	}
	c, err := s.clients(ctx)
	if err != nil {
		return err
	}
	return s.dropHost(ctx, c, hostname, held)
}

func (s *stack) dropHost(ctx context.Context, c Clients, hostname string, held hostRule) error {
	if err := s.deleteRule(ctx, c, held.Rule); err != nil {
		return err
	}
	delete(s.recorded.Hosts, hostname)
	if held.Certificate == "" {
		return nil
	}
	answering, err := s.readFront(ctx, c)
	var refused refusal.Refusal
	if errors.As(err, &refused) && refused.Code == refusal.CodeNotReady {
		return nil
	}
	if err != nil {
		return err
	}
	return s.releaseCertificate(ctx, c, answering.listener, held.Certificate)
}

func (s *stack) releaseCertificate(ctx context.Context, c Clients, listener, certificate string) error {
	for _, held := range s.recorded.Hosts {
		if held.Certificate == certificate {
			return nil
		}
	}
	if _, err := c.Balancers.RemoveListenerCertificates(ctx, &elbv2.RemoveListenerCertificatesInput{
		ListenerArn:  aws.String(listener),
		Certificates: []elbv2types.Certificate{{CertificateArn: aws.String(certificate)}},
	}); err != nil {
		return fmt.Errorf("stop answering with certificate %s: %w", certificate, err)
	}
	return nil
}

func (s *stack) MovePointer(ctx context.Context, move router.PointerMove, _ progress.Log) error {
	c, err := s.clients(ctx)
	if err != nil {
		return router.Unserved{Err: err}
	}
	groups, err := readTargetGroups(ctx, c, s.state.Tier, move.Records)
	if err != nil {
		return router.Unserved{Err: err}
	}
	if err := move.RefuseInactive(ctx); err != nil {
		return err
	}
	pointer := router.ResolvePointer(move.Pointer)
	for _, app := range slices.Sorted(maps.Keys(groups)) {
		for _, hostname := range s.listHosts(pointer, app) {
			if err := s.flipRule(ctx, c, s.recorded.Hosts[hostname].Rule, groups[app], move.RefuseInactive); err != nil {
				return err
			}
		}
	}
	if s.recorded.Served == nil {
		s.recorded.Served = map[string]map[string]string{}
	}
	if s.recorded.Served[pointer] == nil {
		s.recorded.Served[pointer] = map[string]string{}
	}
	maps.Copy(s.recorded.Served[pointer], groups)
	return nil
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

func (s *stack) RemovePointer(ctx context.Context, pointer string, _ progress.Log) error {
	resolved := router.ResolvePointer(pointer)
	hosts := s.listHosts(resolved, "")
	delete(s.recorded.Served, resolved)
	if len(hosts) == 0 {
		return nil
	}
	c, err := s.clients(ctx)
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
		errs = append(errs, s.RemovePointer(ctx, pointer, progress.Discard()))
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
