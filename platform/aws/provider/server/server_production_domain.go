package server

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/service/ssm"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
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
	session.ask = func(headline string, records []edge.Record, notes ...string) {
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
	ssm           bootstrap.SSMAPI
	slug          string
	kind          edge.Kind
	servesUnbound bool

	stack      edge.EdgeStack
	writer     edge.DNSWriter
	poller     dns.Poller
	prober     certs.Prober
	issuer     certs.Issuer
	discarder  func(certs.Certificate) certs.Issuer
	open       func(edge.Kind) (edge.EdgeStack, error)
	ttl        time.Duration
	pins       map[string]string
	zone       string
	configured []string
	host       string
	recorded   bootstrap.Production
	superseded certs.Settlement
	ask        askDNS
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

	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return nil, err
	}
	edgeFront, err := s.edge(edge.Kind(req.edgeKind), awscfg.Region)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	ssmClient := ssm.NewFromConfig(awscfg)

	state, err := bootstrap.ReadStackState(ctx, ssmClient, req.slug)
	if err != nil {
		return nil, err
	}
	if len(state) == 0 {
		return nil, errNoProductionDeploy
	}
	stack, err := edgeFront.Open(state)
	if err != nil {
		return nil, err
	}
	recorded, err := bootstrap.ReadProduction(state)
	if err != nil {
		return nil, err
	}
	writer, err := dns.WriterFor(req.dns.GetKind(), req.dns.GetZone(), dns.Deps{AWS: awscfg})
	if err != nil {
		return nil, err
	}

	session := &domainSession{
		ssm:           ssmClient,
		slug:          req.slug,
		kind:          edgeFront.Kind(),
		servesUnbound: edge.ServesUnbound(edgeFront),

		stack:  stack,
		writer: writer,
		poller: dns.NewPoller(),
		prober: certs.NewProber(),
		discarder: func(cert certs.Certificate) certs.Issuer {
			return certs.DiscardIssuerFor(cert, certs.Deps{AWS: awscfg})
		},
		open: func(kind edge.Kind) (edge.EdgeStack, error) {
			front, err := s.edge(kind, awscfg.Region)
			if err != nil {
				return nil, err
			}
			return front.Open(state)
		},
		ttl:        edge.WriteTTL(writer),
		pins:       normalizePins(opts.Certificates),
		zone:       req.dns.GetZone(),
		configured: configured,
		host:       host,
		recorded:   recorded,
	}
	if req.certificate {
		session.issuer = certs.IssuerFor(edgeFront, certs.Deps{AWS: awscfg})
	}
	return session, nil
}

func (d *domainSession) save(ctx context.Context) error {
	next, err := bootstrap.WithProduction(d.stack.State(), d.recorded)
	if err != nil {
		return err
	}
	return bootstrap.WriteStackStateFor(ctx, d.ssm, bootstrap.ClassProduction, d.slug, next)
}

func (d *domainSession) flow(front []edge.Record) certs.Flow {
	return certs.Flow{Issuer: d.issuer, Writer: d.writer, Prober: d.prober, Front: front, Ask: d.proveOwnership}
}

func (d *domainSession) proveOwnership(records []edge.Record) {
	if d.ask == nil {
		return
	}
	d.ask(
		fmt.Sprintf("Prove you own %s", strings.Join(d.unpinned(), ", ")),
		records,
		"Leave it in place: ACM renews the certificate through it.",
	)
}

func (d *domainSession) pointAtEdge(host string) func([]edge.Record) {
	return func(records []edge.Record) {
		if d.ask == nil {
			return
		}
		d.ask(fmt.Sprintf("Point %s at the %s edge", host, d.kind), records)
	}
}

func (d *domainSession) add(ctx context.Context, progress func(string)) error {
	if len(d.configured) == 0 {
		return fmt.Errorf("this project declares no domains.production, so there is no production hostname to add; declare one in the config and run this again — no command edits the config")
	}
	if d.host != "" && !slices.Contains(d.configured, d.host) {
		return fmt.Errorf("this project does not declare %q: add it to domains.production and run this again — no command edits the config, which declares %s", d.host, strings.Join(d.configured, ", "))
	}
	if err := d.settleCertificate(ctx, progress); err != nil {
		return err
	}
	targets := d.addTargets()
	if len(targets) == 0 {
		progress(fmt.Sprintf("Every hostname this project declares is already served: %s", strings.Join(d.configured, ", ")))
		return d.discardSuperseded(ctx, progress)
	}
	for _, host := range targets {
		if err := d.addHost(ctx, host, progress); err != nil {
			return err
		}
	}
	return d.discardSuperseded(ctx, progress)
}

func (d *domainSession) addTargets() []string {
	if d.host != "" {
		return []string{d.host}
	}
	var targets []string
	for _, host := range d.configured {
		if !d.recorded.Ready(host, d.kind) || d.recorded.Host(host).Certificate != d.wantedARN(host) {
			targets = append(targets, host)
		}
	}
	return targets
}

func (d *domainSession) wantedARN(host string) string {
	if pinned := d.pins[host]; pinned != "" {
		return pinned
	}
	return d.recorded.Certificate.ARN
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

func (d *domainSession) settleCertificate(ctx context.Context, progress func(string)) error {
	wanted := d.unpinned()
	if d.issuer.API == nil || len(wanted) == 0 {
		return nil
	}
	d.superseded = d.recorded.Settlement()
	settled, err := d.flow(nil).Certificate(ctx, wanted, d.recorded.Settlement(), progress, func(settled certs.Settlement) error {
		d.recorded = d.recorded.WithSettlement(settled)
		return d.save(ctx)
	})
	d.recorded = d.recorded.WithSettlement(settled)
	return err
}

func (d *domainSession) certificateFor(ctx context.Context, host string) (string, error) {
	if pinned := d.pins[host]; pinned != "" {
		if d.issuer.API == nil {
			return "", nil
		}
		cert, err := d.issuer.Pinned(ctx, host, pinned)
		if err != nil {
			return "", err
		}
		return cert.ARN, nil
	}
	return d.recorded.Certificate.ARN, nil
}

func (d *domainSession) addHost(ctx context.Context, host string, progress func(string)) error {
	certificate, err := d.certificateFor(ctx, host)
	if err != nil {
		return err
	}
	provisioned := d.recorded.Host(host)
	serving := servingEdge(provisioned)
	provisioned.Certificate = certificate
	d.recorded = d.recorded.WithHost(provisioned)
	if err := d.save(ctx); err != nil {
		return err
	}

	progress(fmt.Sprintf("Binding %s to the %s edge", host, d.kind))
	if err := d.stack.BindDomain(ctx, edge.DomainBinding{Hostname: host, Certificate: certificate}); err != nil {
		return err
	}
	if err := d.save(ctx); err != nil {
		return err
	}

	records, err := edge.RecordsFor(edge.TargetOf(d.kind, d.servesUnbound, d.stack.State()), []string{host})
	if err != nil {
		return err
	}
	for _, rec := range records {
		if note := rec.ApexNote(d.zoneOf(ctx, rec.Name)); note != "" {
			progress(note)
		}
	}
	if err := settleRecords(ctx, d.writer, d.poller, records, progress, d.pointAtEdge(host), func(written, owed []edge.Record) error {
		provisioned.Written, provisioned.Owed = written, owed
		d.recorded = d.recorded.WithHost(provisioned)
		return d.save(ctx)
	}); err != nil {
		return err
	}

	settled, err := d.flow(provisioned.Owed).Probe(ctx, host, d.kind, certs.Settlement{Owed: d.recorded.Owed, Probe: provisioned.Probe}, progress, func(settled certs.Settlement) error {
		provisioned.Probe = settled.Probe
		d.recorded = d.recorded.WithHost(provisioned)
		return d.save(ctx)
	})
	provisioned.Probe = settled.Probe
	d.recorded = d.recorded.WithHost(provisioned)
	if err != nil {
		return err
	}
	progress(fmt.Sprintf("%s is served by the %s edge", host, d.kind))
	return d.retire(ctx, host, serving, progress)
}

func servingEdge(provisioned bootstrap.Provisioned) edge.Kind {
	if !provisioned.Probe.OK {
		return ""
	}
	return provisioned.Probe.Edge
}

func (d *domainSession) retire(ctx context.Context, host string, serving edge.Kind, progress func(string)) error {
	if serving == "" || serving == d.kind || d.open == nil {
		return nil
	}
	stack, err := d.open(serving)
	if err != nil {
		return err
	}
	progress(fmt.Sprintf("Unbinding %s from the %s edge it moved off", host, serving))
	if err := stack.UnbindDomain(ctx, host); err != nil {
		return err
	}
	progress(fmt.Sprintf("%s answers on both edges until resolvers drop the record they hold: %s", host, d.flipWindow()))
	return nil
}

func (d *domainSession) flipWindow() string {
	if d.ttl <= 0 {
		return "whatever TTL your DNS provider serves that record with"
	}
	return d.ttl.String()
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
	for _, host := range targets {
		if err := d.removeHost(ctx, host, progress); err != nil {
			return err
		}
	}
	return d.discardUnused(ctx, progress)
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

func (d *domainSession) removeHost(ctx context.Context, host string, progress func(string)) error {
	recorded := d.recorded.Host(host)

	progress(fmt.Sprintf("Unbinding %s from the %s edge", host, d.kind))
	if err := d.stack.UnbindDomain(ctx, host); err != nil {
		return err
	}
	if err := dns.Release(ctx, d.writer, recorded.Written, progress); err != nil {
		return err
	}
	d.recorded = d.recorded.WithoutHost(host)
	return d.save(ctx)
}

func (d *domainSession) discardUnused(ctx context.Context, progress func(string)) error {
	if d.recorded.Certificate.ARN == "" || len(d.recorded.Hosts) > 0 {
		return nil
	}
	return d.discard(ctx, d.recorded.Settlement(), progress, func() {
		d.recorded = d.recorded.WithSettlement(certs.Settlement{})
	})
}

func (d *domainSession) discardSuperseded(ctx context.Context, progress func(string)) error {
	cert := d.superseded.Certificate
	if cert.ARN == "" || cert.ARN == d.recorded.Certificate.ARN || d.recorded.Uses(cert.ARN) {
		return nil
	}
	settled := d.superseded
	settled.Written = edge.Unwritten(settled.Written, d.recorded.Written)
	return d.discard(ctx, settled, progress, func() { d.superseded = certs.Settlement{} })
}

func (d *domainSession) discard(ctx context.Context, settled certs.Settlement, progress func(string), forget func()) error {
	if err := d.discarder(settled.Certificate).Discard(ctx, settled.Certificate, progress); err != nil {
		return err
	}
	if err := dns.Release(ctx, d.writer, settled.Written, progress); err != nil {
		return err
	}
	forget()
	return d.save(ctx)
}

func (d *domainSession) zoneOf(ctx context.Context, hostname string) string {
	finder, ok := d.writer.(edge.ZoneFinder)
	if !ok {
		return d.zone
	}
	zone, err := finder.ZoneOf(ctx, hostname)
	if err != nil {
		return d.zone
	}
	return zone.Name
}

func releaseProductionDomains(ctx context.Context, state edge.StackState, writer edge.DNSWriter, discarder func(certs.Certificate) certs.Issuer, progress func(string)) error {
	recorded, err := bootstrap.ReadProduction(state)
	if err != nil {
		return err
	}
	var errs []error
	for _, host := range recorded.Hosts {
		if err := dns.Release(ctx, writer, host.Written, progress); err != nil {
			errs = append(errs, err)
		}
	}
	if err := dns.Release(ctx, writer, recorded.Written, progress); err != nil {
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
