package providerserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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
	session, err := h.openEdgeSession(ctx, edge.ClassProduction, req.GetSlug(), req.GetEdge())
	if err != nil {
		return nil, err
	}
	return &hostnames{edgeSession: session, configured: configured, host: host, live: req.GetProbe()}, nil
}

func (h *handlers) AddHostname(ctx context.Context, req *contractv1.HostnameRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	return streamed(ctx, stream, naming.UnitEdge, edgeUnitTitle, progressv1.Phase_PHASE_PROVISIONING, func(sender *eventStream, progress edge.Progress) error {
		session, err := h.hostnames(ctx, req)
		if err != nil {
			return err
		}
		session.cutover.waitForManualRecords(sender)
		return session.add(ctx, progress)
	})
}

func (d *hostnames) add(ctx context.Context, progress edge.Progress) error {
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
	return slices.DeleteFunc(slices.Clone(d.configured), func(held ConfiguredHost) bool { return held.Hostname != d.host })
}

func (d *hostnames) attachHostname(ctx context.Context, target ConfiguredHost, progress edge.Progress) (bool, error) {
	host := target.Hostname
	hostState := d.state.Host(host)
	serving := hostState.Serving()
	held := hostState.Certificate.ID
	certifying := d.hostCertificates(host, &hostState)

	if err := certifying.certify(ctx, host, progress); err != nil {
		return false, err
	}
	if d.state.Ready(host, d.cutover.kind) && hostState.Certificate.ID == held {
		return false, certifying.discardSuperseded(ctx, progress)
	}

	progress.Say(fmt.Sprintf("Binding %s to the %s edge", host, d.cutover.kind))
	if err := d.stack.BindDomain(ctx, edge.DomainBinding{Hostname: host, Certificate: hostState.Certificate.ID, App: target.App, Say: progress.Say}); err != nil {
		return true, err
	}
	if err := d.checkpoint(ctx); err != nil {
		return true, err
	}
	if err := certifying.discardSuperseded(ctx, progress); err != nil {
		return true, err
	}

	records, err := d.cutover.recordsFor(d.stack.State(), host)
	if err != nil {
		return true, err
	}
	written, err := d.cutover.write(ctx, records,
		fmt.Sprintf("Point %s at the %s edge", host, d.cutover.kind), progress.Say)
	hostState.Written, hostState.Manual = written.Written, written.Manual
	d.state.SetHost(host, hostState)
	if cerr := d.checkpoint(ctx); cerr != nil {
		return true, errors.Join(err, cerr)
	}
	if err != nil {
		return true, err
	}

	probe, err := d.cutover.await(ctx, host, progress.Say)
	hostState.Probe = probe
	d.state.SetHost(host, hostState)
	if cerr := d.checkpoint(ctx); cerr != nil {
		return true, errors.Join(err, cerr)
	}
	if err != nil {
		return true, err
	}
	progress.Say(fmt.Sprintf("%s is served by the %s edge", host, d.cutover.kind))
	return true, d.unbindPreviousEdge(ctx, host, serving, progress)
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

func (d *hostnames) unbindPreviousEdge(ctx context.Context, host string, serving edge.Kind, progress edge.Progress) error {
	if serving == "" || serving == d.cutover.kind {
		return nil
	}
	stack, err := d.on(serving)
	if err != nil {
		return err
	}
	progress.Say(fmt.Sprintf("Unbinding %s from the %s edge it moved off", host, serving))
	if err := edge.Heeded(stack.UnbindDomain(ctx, host), progress); err != nil {
		return err
	}
	progress.Say(fmt.Sprintf("%s answers on both edges until resolvers drop the record they hold: %s",
		host, flipWindow(d.cutover.dns)))
	return nil
}

func (h *handlers) RemoveHostname(ctx context.Context, req *contractv1.HostnameRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	return streamed(ctx, stream, naming.UnitEdge, edgeUnitTitle, progressv1.Phase_PHASE_DELETING, func(_ *eventStream, progress edge.Progress) error {
		session, err := h.hostnames(ctx, req)
		if err != nil {
			return err
		}
		return session.remove(ctx, progress)
	})
}

func (d *hostnames) remove(ctx context.Context, progress edge.Progress) error {
	targets, err := d.removeTargets()
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		if len(d.state.Hosts) == 0 {
			progress.Say("Nothing to remove: this project serves no production hostname")
			return nil
		}
		progress.Say("Nothing to remove: every hostname this project serves is still declared in its config")
		return nil
	}
	for _, host := range targets {
		progress.Say(fmt.Sprintf("Unbinding %s from the %s edge", host, d.cutover.kind))
		if err := edge.Heeded(d.stack.UnbindDomain(ctx, host), progress); err != nil {
			return err
		}
		hostState := d.state.Host(host)
		if err := d.cutover.release(ctx, hostState.Written, progress.Say); err != nil {
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
			if err := retireCertificate(ctx, d.provider, d.cutover, cert, provider.Certificate{}, progress); err != nil {
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
	held := d.stack.State()
	bound := edge.Pointable(edge.TargetOf(d.cutover.kind, d.cutover.unbound, held), held.Bound, host)

	var manual []edge.Record
	if bound {
		wanted, err := d.cutover.recordsFor(held, host)
		if err != nil {
			return nil, err
		}
		manual = edge.Unwritten(wanted, hostState.Written)
	}

	probe := hostState.Probe
	if bound && d.live {
		probe = d.probe(ctx, host)
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
		ServingPointer: servingPointer(probe, d.cutover.kind),
	}
	row.Pending = d.hostnameBlocker(host, hostState.Certificate, health, bound, probe)
	row.Ready = row.GetPending() == ""
	return row, nil
}

func (d *hostnames) probe(ctx context.Context, host string) stackrecords.ServeProbe {
	serving, err := d.cutover.attempt(ctx, host)
	return stackrecords.ServeProbe{At: d.cutover.now().Unix(), OK: err == nil && serving == d.cutover.kind, Edge: serving}
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
		return fmt.Sprintf("%s is not bound to the %s edge yet; run `ocel domain add`", host, d.cutover.kind)
	case !probe.OK:
		return fmt.Sprintf("%s does not answer as the %s edge yet%s", host, d.cutover.kind, d.cutover.unreached(host))
	}
	return ""
}

func servingPointer(probe stackrecords.ServeProbe, kind edge.Kind) string {
	if probe.OK && probe.Edge != "" {
		return string(probe.Edge)
	}
	return string(kind)
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
		LastProbeEdge:     string(probe.Edge),
	}
}
