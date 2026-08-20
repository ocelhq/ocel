package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	connect "connectrpc.com/connect"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	"github.com/ocelhq/ocel/platform/aws/provider/domains"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (s *Server) AddDomain(ctx context.Context, req *deploymentsv1.AddDomainRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	progress := func(m string) { _ = stream.Send(progressEvent(m)) }

	session, err := s.domainSession(ctx, domainRequest{
		options:     req.GetOptions(),
		slug:        req.GetSlug(),
		edgeKind:    string(requestedEdge(req)),
		dns:         requestedDNS(req),
		configured:  req.GetConfigured(),
		host:        req.GetHost(),
		certificate: true,
	})
	if err != nil {
		return stream.Send(failureResult(err))
	}
	session.engine.Ask = func(headline string, records []edge.Record, notes ...string) {
		_ = stream.Send(dnsOwedEvent(headline, records, notes...))
	}
	if err := session.add(ctx, progress); err != nil {
		return stream.Send(failureResult(err))
	}
	return stream.Send(okResult())
}

func (s *Server) RemoveDomain(ctx context.Context, req *deploymentsv1.RemoveDomainRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	progress := func(m string) { _ = stream.Send(progressEvent(m)) }

	session, err := s.domainSession(ctx, domainRequest{
		options:    req.GetOptions(),
		slug:       req.GetSlug(),
		edgeKind:   string(requestedEdge(req)),
		dns:        requestedDNS(req),
		configured: req.GetConfigured(),
		host:       req.GetHost(),
	})
	if err != nil {
		return stream.Send(failureResult(err))
	}
	if err := session.remove(ctx, progress); err != nil {
		return stream.Send(failureResult(err))
	}
	return stream.Send(okResult())
}

type domainRequest struct {
	options     []byte
	slug        string
	edgeKind    string
	dns         *deploymentsv1.Dns
	configured  []string
	host        string
	certificate bool
}

type domainSession struct {
	engine     domains.Engine
	stack      edge.EdgeStack
	recorded   domains.Settlement
	configured []string
	host       string
	pins       map[string]string
}

type stackWriter struct {
	ssm     bootstrap.SSMAPI
	slug    string
	stack   edge.EdgeStack
	settled domains.Settlement
}

func (w *stackWriter) Save(ctx context.Context, settled domains.Settlement) error {
	w.settled = settled
	return w.write(ctx)
}

func (w *stackWriter) write(ctx context.Context) error {
	return bootstrap.WriteStackRecordFor(ctx, w.ssm, bootstrap.ClassProduction, w.slug, bootstrap.StackRecord{
		Edge:       w.stack.State(),
		Production: w.settled,
	})
}

type persistingStack struct {
	edge.EdgeStack
	written edge.StackState
	save    func(context.Context) error
}

func persisting(stack edge.EdgeStack, save func(context.Context) error) *persistingStack {
	return &persistingStack{EdgeStack: stack, written: stack.State(), save: save}
}

func (p *persistingStack) BindDomain(ctx context.Context, binding edge.DomainBinding) error {
	return p.persist(ctx, p.EdgeStack.BindDomain(ctx, binding))
}

func (p *persistingStack) UnbindDomain(ctx context.Context, hostname string) error {
	return p.persist(ctx, p.EdgeStack.UnbindDomain(ctx, hostname))
}

func (p *persistingStack) persist(ctx context.Context, err error) error {
	held := p.EdgeStack.State()
	if held.Equal(p.written) {
		return err
	}
	p.written = held
	return errors.Join(err, p.save(ctx))
}

func (s *Server) domainSession(ctx context.Context, req domainRequest) (*domainSession, error) {
	opts, err := parseOptions(req.options)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if req.slug == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("a production domain belongs to one project; this request names none"))
	}
	configured, err := productionHosts(req.configured)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	host, err := productionHost(req.host)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	clients, err := s.domainClients(ctx, opts.Region)
	if err != nil {
		return nil, err
	}
	edgeFront, err := s.edge(edge.Kind(req.edgeKind), clients.region)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	record, err := bootstrap.ReadStackRecord(ctx, clients.ssm, req.slug)
	if err != nil {
		return nil, err
	}
	if record.Empty() {
		return nil, errNoProductionDeploy
	}
	opened, err := edgeFront.Open(record.Edge)
	if err != nil {
		return nil, err
	}
	recorded := record.Production
	writer, err := clients.writerFor(req.dns.GetKind(), req.dns.GetZone())
	if err != nil {
		return nil, err
	}

	held := &stackWriter{ssm: clients.ssm, slug: req.slug, stack: opened, settled: recorded}
	stack := persisting(opened, held.write)
	engine := domains.Engine{
		Kind:          edgeFront.Kind(),
		ServesUnbound: edgeFront.Facts().ServesUnbound,
		Discarder:     clients.discarderFor,
		Writer:        writer,
		Poller:        clients.poller,
		Prober:        clients.prober,
		Store:         held,
		Zone:          req.dns.GetZone(),
		Open: func(kind edge.Kind) (edge.EdgeStack, error) {
			front, err := s.edge(kind, clients.region)
			if err != nil {
				return nil, err
			}
			return front.Open(record.Edge)
		},
		Unbind: func(ctx context.Context, hostname string) error {
			return stack.UnbindDomain(ctx, hostname)
		},
	}
	if req.certificate {
		engine.Issuer = clients.issuerFor(edgeFront)
	}
	return &domainSession{
		engine:     engine,
		stack:      stack,
		recorded:   recorded,
		configured: configured,
		host:       host,
		pins:       normalizePins(opts.Certificates),
	}, nil
}

func (d *domainSession) add(ctx context.Context, progress func(string)) error {
	if len(d.configured) == 0 {
		return fmt.Errorf("this project declares no domains.production, so there is no production hostname to add; declare one in the config and run this again — no command edits the config")
	}
	if d.host != "" && !slices.Contains(d.configured, d.host) {
		return fmt.Errorf("this project does not declare %q: add it to domains.production and run this again — no command edits the config, which declares %s", d.host, strings.Join(d.configured, ", "))
	}
	choose := func(settled domains.Settlement) []domains.Target {
		targets := d.addTargets(settled)
		if len(targets) == 0 {
			progress(fmt.Sprintf("Every hostname this project declares is already served: %s", strings.Join(d.configured, ", ")))
		}
		return targets
	}
	settled, err := d.engine.Settle(ctx, d.recorded, d.unpinned(), choose, progress)
	d.recorded = settled
	return err
}

func (d *domainSession) addTargets(settled domains.Settlement) []domains.Target {
	var hosts []string
	if d.host != "" {
		hosts = []string{d.host}
	} else {
		for _, host := range d.configured {
			if !settled.Ready(host, d.engine.Kind) || settled.Host(host).Certificate != d.wantedARN(settled, host) {
				hosts = append(hosts, host)
			}
		}
	}
	targets := make([]domains.Target, 0, len(hosts))
	for _, host := range hosts {
		targets = append(targets, d.target(host))
	}
	return targets
}

func (d *domainSession) target(host string) domains.Target {
	return domains.Target{
		Hostname: host,
		Pinned:   d.pins[host],
		Surface: func(ctx context.Context, certificate string, say func(string)) (edge.DNSTarget, error) {
			say(fmt.Sprintf("Binding %s to the %s edge", host, d.engine.Kind))
			return d.bind(ctx, host, certificate)
		},
	}
}

func (d *domainSession) bind(ctx context.Context, host, certificate string) (edge.DNSTarget, error) {
	if err := d.stack.BindDomain(ctx, edge.DomainBinding{Hostname: host, Certificate: certificate}); err != nil {
		return edge.DNSTarget{}, err
	}
	return edge.TargetOf(d.engine.Kind, d.engine.ServesUnbound, d.stack.State()), nil
}

func (d *domainSession) wantedARN(settled domains.Settlement, host string) string {
	if pinned := d.pins[host]; pinned != "" {
		return pinned
	}
	return settled.Certificate.ARN
}

func (d *domainSession) unpinned() []string {
	var wanted []string
	for _, host := range d.configured {
		if d.pins[host] == "" {
			wanted = append(wanted, host)
		}
	}
	return wanted
}

func (d *domainSession) remove(ctx context.Context, progress func(string)) error {
	targets, err := d.removeTargets()
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		if len(d.recorded.Hosts) == 0 {
			progress("Nothing to remove: this project serves no production hostname")
			return nil
		}
		progress("Nothing to remove: every hostname this project serves is still declared in its config")
		return nil
	}
	settled, err := d.engine.Withdraw(ctx, d.recorded, targets, progress)
	d.recorded = settled
	return err
}

func (d *domainSession) removeTargets() ([]string, error) {
	provisioned := d.recorded.Hostnames()
	if d.host != "" {
		if !slices.Contains(provisioned, d.host) {
			return nil, fmt.Errorf("this project serves no %q: it serves %s", d.host, provisionedList(provisioned))
		}
		return []string{d.host}, nil
	}
	var targets []string
	for _, host := range provisioned {
		if !slices.Contains(d.configured, host) {
			targets = append(targets, host)
		}
	}
	return targets, nil
}

func provisionedList(hosts []string) string {
	if len(hosts) == 0 {
		return "no production hostname at all"
	}
	return strings.Join(hosts, ", ")
}

func releaseProductionDomains(ctx context.Context, recorded domains.Settlement, writer edge.DNSWriter, discarder func(certs.Certificate) certs.Issuer, progress func(string)) error {
	var errs []error
	for _, host := range recorded.Hosts {
		if err := dns.Release(ctx, writer, host.Records.Written, progress); err != nil {
			errs = append(errs, err)
		}
	}
	if err := dns.Release(ctx, writer, recorded.Validation.Written, progress); err != nil {
		errs = append(errs, err)
	}
	if recorded.Certificate.ARN != "" {
		if err := discarder(recorded.Certificate).Discard(ctx, recorded.Certificate, progress); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func productionHosts(hosts []string) ([]string, error) {
	var out []string
	for _, raw := range hosts {
		host, err := productionHost(raw)
		if err != nil {
			return nil, err
		}
		if host != "" && !slices.Contains(out, host) {
			out = append(out, host)
		}
	}
	return out, nil
}

func productionHost(raw string) (string, error) {
	host := strings.TrimSuffix(strings.TrimSpace(strings.ToLower(raw)), ".")
	switch {
	case host == "":
		return "", nil
	case strings.ContainsAny(host, "/:*"), !strings.Contains(host, "."):
		return "", fmt.Errorf("%q is not a production hostname: pass a name like app.acme.com — a wildcard belongs to domains.preview", raw)
	}
	return host, nil
}

func normalizePins(pins map[string]string) map[string]string {
	if len(pins) == 0 {
		return nil
	}
	out := make(map[string]string, len(pins))
	for host, arn := range pins {
		if arn = strings.TrimSpace(arn); arn != "" {
			out[strings.ToLower(strings.TrimSpace(host))] = arn
		}
	}
	return out
}
