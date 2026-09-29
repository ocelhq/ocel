package providerserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

type hostnames struct {
	*edgeSession
	configured []ConfiguredHost
	host       string
	live       bool
}

func (h *handlers) hostnames(ctx context.Context, req *contractv1.HostnameRequest) (*hostnames, error) {
	configured, err := productionHosts(req.GetConfigured())
	if err != nil {
		return nil, err
	}
	host, err := productionHost(req.GetHost())
	if err != nil {
		return nil, err
	}
	session, err := h.openEdgeSession(ctx, environment.TierProduction, req.GetSlug(), req.GetEdge())
	if err != nil {
		return nil, err
	}
	return &hostnames{edgeSession: session, configured: configured, host: host, live: req.GetProbe()}, nil
}

func (h *handlers) AddHostname(ctx context.Context, req *contractv1.HostnameRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	title := "Attaching " + namedList("production hostname", "production hostnames", requestedHosts(req))
	unit := UnitSpan(naming.UnitEdge, req.GetSlug(), title, progressv1.Phase_PHASE_PROVISION)
	return streamed(ctx, stream, unit, func(sender *eventStream, progress progress.Progress) error {
		session, err := h.hostnames(ctx, req)
		if err != nil {
			return err
		}
		session.cutover.waitForManualRecords(sender, unit)
		return session.add(ctx, progress)
	})
}

func requestedHosts(req *contractv1.HostnameRequest) []string {
	if req.GetHost() != "" {
		return []string{req.GetHost()}
	}
	hosts := make([]string, 0, len(req.GetConfigured()))
	for _, configured := range req.GetConfigured() {
		hosts = append(hosts, configured.GetHostname())
	}
	return hosts
}

func (d *hostnames) add(ctx context.Context, progress progress.Progress) error {
	if len(d.configured) == 0 {
		return refusal.Refuse(refusal.CodeNotReady,
			"this project declares no domains.production, so there is no production hostname to add; declare one in the config and run this again — no command edits the config")
	}
	if d.host != "" && !slices.Contains(d.declared(), d.host) {
		return refusal.Refuse(refusal.CodeInvalid,
			"this project does not declare %q: add it to domains.production and run this again — no command edits the config, which declares %s",
			d.host, strings.Join(d.declared(), ", "))
	}
	promoted, err := d.promoted(ctx)
	if err != nil {
		return err
	}
	if !promoted {
		return refusal.Refuse(refusal.CodeNotReady,
			"this project has promoted no release yet, so nothing would answer at %s: `ocel deploy` promotes one and attaches every hostname it declares",
			strings.Join(hostnamesOf(d.addTargets()), ", "))
	}
	var attachedAny bool
	for _, host := range d.addTargets() {
		changed, err := d.attachHostname(ctx, host, progress)
		if err != nil {
			return err
		}
		attachedAny = attachedAny || changed
	}
	if !attachedAny && d.host == "" {
		progress.Say(fmt.Sprintf("Every hostname this project declares is already served: %s", strings.Join(d.declared(), ", ")))
	}
	return nil
}

func (d *hostnames) declared() []string { return hostnamesOf(d.configured) }

func (d *hostnames) addTargets() []ConfiguredHost {
	if d.host == "" {
		return d.configured
	}
	return slices.DeleteFunc(slices.Clone(d.configured), func(configured ConfiguredHost) bool { return configured.Hostname != d.host })
}

func (d *hostnames) attachHostname(ctx context.Context, target ConfiguredHost, progress progress.Progress) (bool, error) {
	host := target.Hostname
	hostState := d.state.Host(host)
	previous, served := hostState.ServedEdge()
	answering := d.readAppRouter(target.App)
	priorCertID := hostState.Certificate.ID
	certifying := d.hostCertificates(host, &hostState)

	if err := certifying.certify(ctx, host, progress); err != nil {
		return false, err
	}
	if d.state.Ready(host, d.front.Kind(), answering) && hostState.Certificate.ID == priorCertID {
		reclaimed, err := d.refreshOriginClaim(ctx, target, &hostState, progress)
		if err != nil {
			return reclaimed, err
		}
		return reclaimed, certifying.discardSuperseded(ctx, progress)
	}

	if err := d.bindOrigin(ctx, target, &hostState, progress); err != nil {
		return true, err
	}
	if err := certifying.discardSuperseded(ctx, progress); err != nil {
		return true, err
	}

	records, err := d.cutover.recordsFor(d.edgeStack().State(), host)
	if err != nil {
		return true, err
	}
	written, err := d.cutover.write(ctx, records,
		fmt.Sprintf("Point %s at %s", host, describeFront(d.front.Kind())), progress.Say)
	hostState.Written, hostState.Manual = written.Written, written.Manual
	d.state.SetHost(host, hostState)
	if cerr := d.checkpoint(ctx); cerr != nil {
		return true, errors.Join(err, cerr)
	}
	if err != nil {
		return true, err
	}

	probe, err := d.cutover.await(ctx, host, answering, progress.Say)
	hostState.Probe = probe
	d.state.SetHost(host, hostState)
	if cerr := d.checkpoint(ctx); cerr != nil {
		return true, errors.Join(err, cerr)
	}
	if err != nil {
		return true, err
	}
	progress.Say(fmt.Sprintf("%s is served through %s", host, describeFront(d.front.Kind())))
	if !served || previous == d.front.Kind() {
		return true, nil
	}
	return true, d.unbindPreviousEdge(ctx, host, previous, progress)
}

func (d *hostnames) bindOrigin(ctx context.Context, target ConfiguredHost, hostState *stackrecords.HostnameState, progress progress.Progress) error {
	host := target.Hostname
	claimed, err := d.claimRouterOrigin(ctx, target, hostState)
	if err == nil {
		progress.Say(fmt.Sprintf("Binding %s to %s", host, describeFront(d.front.Kind())))
		err = d.edgeStack().BindDomain(ctx, edge.DomainBinding{Hostname: host, Certificate: hostState.Certificate.ID, App: target.App, Origin: claimed.origin, Say: progress.Say})
	}
	if err != nil {
		return errors.Join(err, d.settleUnboundClaim(ctx, target, claimed, hostState, progress))
	}
	hostState.Edge = d.front.Kind()
	superseded := claimed.recordOn(hostState)
	d.state.SetHost(host, *hostState)
	if err := d.checkpoint(ctx); err != nil {
		return err
	}
	revokeOriginCertificate(ctx, d.front, superseded, progress)
	return nil
}

func (d *hostnames) settleUnboundClaim(ctx context.Context, target ConfiguredHost, claimed originClaim, hostState *stackrecords.HostnameState, progress progress.Progress) error {
	if hostState.Edge != "" {
		if claimed.issued.ID == "" {
			return nil
		}
		superseded := claimed.recordOn(hostState)
		d.state.SetHost(target.Hostname, *hostState)
		if err := d.checkpoint(ctx); err != nil {
			return err
		}
		revokeOriginCertificate(ctx, d.front, superseded, progress)
		return nil
	}
	if err := d.disclaim(ctx, target.Hostname); err != nil {
		return err
	}
	revokeOriginCertificate(ctx, d.front, claimed.issued.ID, progress)
	d.state.SetHost(target.Hostname, *hostState)
	return d.checkpoint(ctx)
}

func (d *hostnames) refreshOriginClaim(ctx context.Context, target ConfiguredHost, hostState *stackrecords.HostnameState, progress progress.Progress) (bool, error) {
	if d.routerOrigin() == nil {
		return false, nil
	}
	changed, err := clientCertificatesChanged(ctx, d.front, target.Hostname, hostState.ClientCertificateDigests)
	if err != nil {
		return false, err
	}
	due := isOriginCertificateDue(hostState, time.Now())
	if !changed && !due {
		return false, nil
	}
	if due {
		progress.Say(fmt.Sprintf("Claiming %s again: the certificate its origin answers it with is due for renewal", target.Hostname))
	} else {
		progress.Say(fmt.Sprintf("Claiming %s again: the client certificates %s presents to its origin changed", target.Hostname, describeFront(d.front.Kind())))
	}
	return true, d.bindOrigin(ctx, target, hostState, progress)
}

func (d *hostnames) claimRouterOrigin(ctx context.Context, target ConfiguredHost, hostState *stackrecords.HostnameState) (originClaim, error) {
	if d.routerOrigin() == nil {
		return originClaim{}, nil
	}
	routed, err := d.openRouterStack()
	if err != nil {
		return originClaim{}, err
	}
	claim := router.Claim{Hostname: target.Hostname, App: target.App, Certificate: hostState.Certificate.ID}
	claimed, err := claimOrigin(ctx, d.front, claim, routed.Claim, d.reserveOriginCertificate(target.Hostname, *hostState))
	return claimed, errors.Join(err, d.adopt(routed))
}

func (d *hostnames) reserveOriginCertificate(host string, prior stackrecords.HostnameState) originReservation {
	return func(ctx context.Context, issued edge.OriginCertificate) (func(context.Context) error, error) {
		reserved := prior
		reserved.OriginCertificateID, reserved.OriginCertificateExpiresAt = issued.ID, issued.ExpiresAt
		d.state.SetHost(host, reserved)
		release := func(ctx context.Context) error {
			d.state.SetHost(host, prior)
			return d.checkpoint(ctx)
		}
		if err := d.checkpoint(ctx); err != nil {
			return nil, errors.Join(err, release(ctx))
		}
		return release, nil
	}
}

func (d *hostnames) disclaim(ctx context.Context, hostname string) error {
	if d.routerOrigin() == nil {
		return nil
	}
	routed, err := d.openRouterStack()
	if err != nil {
		return err
	}
	return errors.Join(routed.Disclaim(ctx, hostname), d.adopt(routed))
}

func (d *hostnames) hostCertificates(host string, hostState *stackrecords.HostnameState) hostCertificates {
	return hostCertificates{
		provider:  d.provider,
		cutover:   d.cutover,
		hostState: hostState,
		uses:      func(id string) bool { return d.state.Uses(id) },
		persist: func(ctx context.Context) error {
			d.state.SetHost(host, *hostState)
			return d.checkpoint(ctx)
		},
	}
}

func (d *hostnames) unbindPreviousEdge(ctx context.Context, host string, previous edge.Kind, runProgress progress.Progress) error {
	stack, err := d.on(previous)
	if err != nil {
		return err
	}
	runProgress.Say(fmt.Sprintf("Unbinding %s from %s it moved off", host, describeFront(previous)))
	if err := progress.Heeded(stack.UnbindDomain(ctx, host), runProgress); err != nil {
		return err
	}
	runProgress.Say(fmt.Sprintf("%s answers on both fronts until resolvers drop the record they cached: %s",
		host, flipWindow(d.cutover.dns)))
	return nil
}

func (h *handlers) RemoveHostname(ctx context.Context, req *contractv1.HostnameRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	title := "Detaching every production hostname no longer declared"
	if req.GetHost() != "" {
		title = "Detaching " + req.GetHost() + " from production"
	}
	unit := UnitSpan(naming.UnitEdge, req.GetSlug(), title, progressv1.Phase_PHASE_DESTROY)
	return streamed(ctx, stream, unit, func(_ *eventStream, progress progress.Progress) error {
		session, err := h.hostnames(ctx, req)
		if err != nil {
			return err
		}
		return session.remove(ctx, progress)
	})
}

func (d *hostnames) remove(ctx context.Context, runProgress progress.Progress) error {
	targets, err := d.removeTargets()
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		if len(d.state.Hosts) == 0 {
			runProgress.Say("Nothing to remove: this project serves no production hostname")
			return nil
		}
		runProgress.Say("Nothing to remove: every hostname this project serves is still declared in its config")
		return nil
	}
	for _, host := range targets {
		runProgress.Say(fmt.Sprintf("Unbinding %s from %s", host, describeFront(d.front.Kind())))
		if err := progress.Heeded(d.edgeStack().UnbindDomain(ctx, host), runProgress); err != nil {
			return err
		}
		if err := d.disclaim(ctx, host); err != nil {
			return err
		}
		hostState := d.state.Host(host)
		revokeOriginCertificate(ctx, d.front, hostState.OriginCertificateID, runProgress)
		if err := d.cutover.release(ctx, hostState.Written, runProgress.Say); err != nil {
			return err
		}
		d.state.Forget(host)
		if err := d.checkpoint(ctx); err != nil {
			return err
		}
		for _, cert := range hostState.Certificates() {
			if d.state.Uses(cert.ID) {
				continue
			}
			if err := discardCertificateAndRecords(ctx, d.provider, d.cutover, cert, provider.Certificate{}, runProgress); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *hostnames) removeTargets() ([]string, error) {
	provisioned := d.state.Hostnames()
	if d.host != "" {
		if !slices.Contains(provisioned, d.host) {
			return nil, refusal.Refuse(refusal.CodeInvalid, "this project serves no %q: it serves %s", d.host, provisionedList(provisioned))
		}
		return []string{d.host}, nil
	}
	var targets []string
	for _, host := range provisioned {
		if !slices.Contains(d.declared(), host) {
			targets = append(targets, host)
		}
	}
	return targets, nil
}

func (h *handlers) GetHostnameStatus(ctx context.Context, req *contractv1.HostnameRequest) (*contractv1.GetHostnameStatusResponse, error) {
	session, err := h.hostnames(ctx, req)
	if undeployed(err) {
		return declaredHostnames(req), nil
	}
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	resp, err := session.status(ctx)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	return resp, nil
}

func declaredHostnames(req *contractv1.HostnameRequest) *contractv1.GetHostnameStatusResponse {
	resp := &contractv1.GetHostnameStatusResponse{}
	for _, hostname := range req.GetConfigured() {
		resp.Hostnames = append(resp.Hostnames, &contractv1.ProductionHostname{Hostname: hostname.GetHostname(), Declared: true})
	}
	return resp
}

func (d *hostnames) status(ctx context.Context) (*contractv1.GetHostnameStatusResponse, error) {
	resp := &contractv1.GetHostnameStatusResponse{Ready: len(d.configured) > 0}
	for _, host := range d.statusHosts() {
		row, err := d.statusOf(ctx, host)
		if err != nil {
			return nil, err
		}
		resp.Hostnames = append(resp.Hostnames, row)
		if row.GetDeclared() && !row.GetReady() {
			resp.Ready = false
		}
	}
	return resp, nil
}

func (d *hostnames) statusHosts() []string {
	hosts := d.declared()
	for _, host := range d.state.Hostnames() {
		if !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	slices.Sort(hosts)
	return hosts
}

func (d *hostnames) statusOf(ctx context.Context, host string) (*contractv1.ProductionHostname, error) {
	hostState := d.state.Host(host)
	stackState := d.edgeStack().State()
	bound := edge.Pointable(edge.TargetOf(d.cutover.kind, d.cutover.facts, stackState), stackState.Bound, host)

	var manual []edge.Record
	if bound {
		wanted, err := d.cutover.recordsFor(stackState, host)
		if err != nil {
			return nil, err
		}
		manual = edge.Unwritten(wanted, hostState.Written)
	}

	probe := hostState.Probe
	if bound && d.live {
		probe = d.probe(ctx, host, d.readAppRouter(d.findApp(host)))
	}
	health, err := d.provider.Certificates().Inspect(ctx, d.cutover.kind, host, hostState.Certificate)
	if err != nil {
		return nil, err
	}
	row := &contractv1.ProductionHostname{
		Hostname:       host,
		Declared:       slices.Contains(d.declared(), host),
		Certificate:    certificateState(hostState, probe, manual, health.Status),
		RenewalStatus:  health.Renewal,
		ExpiresAt:      health.ExpiresAt,
		ExpiringSoon:   health.ExpiringSoon,
		ServingPointer: servingPointer(hostState, d.front.Kind()),
	}
	row.Pending = d.hostnameBlocker(host, hostState.Certificate, health, bound, probe)
	row.Ready = row.GetPending() == ""
	return row, nil
}

func (d *hostnames) findApp(host string) string {
	for _, configured := range d.configured {
		if configured.Hostname == host {
			return configured.App
		}
	}
	return ""
}

func (d *hostnames) probe(ctx context.Context, host string, answering router.Kind) stackrecords.ServeProbe {
	serving, err := d.cutover.attempt(ctx, host)
	probe := stackrecords.ServeProbe{At: d.cutover.now().Unix(), Router: serving}
	probe.OK = err == nil && probe.IsAnsweredBy(answering)
	return probe
}

func (d *hostnames) hostnameBlocker(host string, cert provider.Certificate, health provider.CertificateHealth, bound bool, probe stackrecords.ServeProbe) string {
	switch {
	case !slices.Contains(d.declared(), host):
		return fmt.Sprintf("this project no longer declares %s; `ocel domain rm` gives it back", host)
	case health.Terminates && !cert.Issued():
		return fmt.Sprintf("no certificate covers %s yet; run `ocel domain add`", host)
	case health.Terminates && !health.Issued:
		return fmt.Sprintf("certificate %s is %s, not issued", cert.ID, certificateStatusWord(health.Status))
	case health.Terminates && !health.Covers:
		return fmt.Sprintf("certificate %s covers %s, which does not include %s",
			cert.ID, strings.Join(health.Domains, ", "), host)
	case !bound:
		return fmt.Sprintf("%s is not bound to %s yet; run `ocel domain add`", host, describeFront(d.cutover.kind))
	case !probe.OK:
		return fmt.Sprintf("%s does not answer through %s yet%s", host, describeFront(d.cutover.kind), d.cutover.lastProbeFailure(host))
	}
	return ""
}

func servingPointer(host stackrecords.HostnameState, front edge.Kind) string {
	if bound, served := host.ServedEdge(); served {
		return string(bound)
	}
	return string(front)
}

func certificateStatusWord(status string) string {
	if status == "" {
		return "in no state the provider reports"
	}
	return strings.ToLower(status)
}

func certificateState(hostState stackrecords.HostnameState, probe stackrecords.ServeProbe, manual []edge.Record, status string) *contractv1.CertificateState {
	return &contractv1.CertificateState{
		CertificateId:     hostState.Certificate.ID,
		CertificateStatus: status,
		RecordsWritten:    recordLines(hostState.WrittenRecords()),
		ManualRecords:     recordLines(append(hostState.ManualRecords(), manual...)),
		LastProbeAt:       probe.At,
		LastProbeOk:       probe.OK,
	}
}

type ConfiguredHost struct {
	Hostname string
	App      string
}

func productionHosts(hosts []*contractv1.ConfiguredHostname) ([]ConfiguredHost, error) {
	var out []ConfiguredHost
	for _, raw := range hosts {
		host, err := productionHost(raw.GetHostname())
		if err != nil {
			return nil, err
		}
		if host == "" || slices.ContainsFunc(out, func(configured ConfiguredHost) bool { return configured.Hostname == host }) {
			continue
		}
		out = append(out, ConfiguredHost{Hostname: host, App: raw.GetApp()})
	}
	return out, nil
}

func hostnamesOf(hosts []ConfiguredHost) []string {
	named := make([]string, 0, len(hosts))
	for _, host := range hosts {
		named = append(named, host.Hostname)
	}
	return named
}

func productionHost(raw string) (string, error) {
	host := strings.TrimSuffix(strings.TrimSpace(strings.ToLower(raw)), ".")
	switch {
	case host == "":
		return "", nil
	case strings.ContainsAny(host, "/:*"), !strings.Contains(host, "."):
		return "", refusal.Refuse(refusal.CodeInvalid,
			"%q is not a production hostname: pass a name like app.acme.com — a wildcard belongs to domains.preview", raw)
	}
	return host, nil
}

func previewBaseDomain(raw string) (string, error) {
	base := strings.TrimSuffix(strings.TrimSpace(strings.ToLower(raw)), ".")
	switch {
	case base == "":
		return "", refusal.Refuse(refusal.CodeInvalid, "a domain is required, e.g. `ocel domain use --preview preview.acme.com`")
	case strings.HasPrefix(base, "*."):
		return "", refusal.Refuse(refusal.CodeInvalid,
			"give the domain itself, not the wildcard: every preview is served on its own subdomain of it, so pass %q",
			strings.TrimPrefix(base, "*."))
	case strings.ContainsAny(base, "/:*"), !strings.Contains(base, "."):
		return "", refusal.Refuse(refusal.CodeInvalid, "%q is not a domain name: pass a hostname like preview.acme.com", raw)
	}
	return base, nil
}

func provisionedList(hosts []string) string {
	if len(hosts) == 0 {
		return "no production hostname at all"
	}
	return strings.Join(hosts, ", ")
}

func recordLines(records []edge.Record) []string {
	var out []string
	for _, rec := range records {
		if line := rec.String(); !slices.Contains(out, line) {
			out = append(out, line)
		}
	}
	return out
}

func flipWindow(records edge.DNSRecords) string {
	if records == nil {
		return unknownTTL
	}
	if ttl := records.TTL(); ttl > 0 {
		return ttl.String()
	}
	return unknownTTL
}

const unknownTTL = "whatever TTL your DNS provider serves that record with"
