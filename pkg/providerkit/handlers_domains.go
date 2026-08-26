package providerkit

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
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type hostnames struct {
	*stackSession
	configured []string
	host       string
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
	session, err := h.openStack(ctx, ClassProduction, req.GetSlug(), req.GetEdge())
	if err != nil {
		return nil, err
	}
	return &hostnames{stackSession: session, configured: configured, host: host}, nil
}

func (h *handlers) AddHostname(ctx context.Context, req *contractv1.HostnameRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	return streamed(ctx, stream, naming.UnitEdge, edgeUnitTitle, progressv1.Phase_PHASE_PROVISIONING, func(sender *eventSender, report Reporter) error {
		session, err := h.hostnames(ctx, req)
		if err != nil {
			return err
		}
		session.settle.ask = func(headline string, records []edge.Record, notes ...string) {
			sender.send(dnsOwedEvent(headline, records, notes...))
		}
		return session.add(ctx, report)
	})
}

func (d *hostnames) add(ctx context.Context, report Reporter) error {
	if len(d.configured) == 0 {
		return Refuse(CodeNotReady,
			"this project declares no domains.production, so there is no production hostname to add; declare one in the config and run this again — no command edits the config")
	}
	if d.host != "" && !slices.Contains(d.configured, d.host) {
		return Refuse(CodeInvalid,
			"this project does not declare %q: add it to domains.production and run this again — no command edits the config, which declares %s",
			d.host, strings.Join(d.configured, ", "))
	}
	var settledAny bool
	for _, host := range d.addTargets() {
		changed, err := d.settleHost(ctx, host, report)
		if err != nil {
			return err
		}
		settledAny = settledAny || changed
	}
	if !settledAny && d.host == "" {
		report.Say(fmt.Sprintf("Every hostname this project declares is already served: %s", strings.Join(d.configured, ", ")))
	}
	return nil
}

func (d *hostnames) addTargets() []string {
	if d.host != "" {
		return []string{d.host}
	}
	return d.configured
}

func (d *hostnames) settleHost(ctx context.Context, host string, report Reporter) (bool, error) {
	settled := d.state.Host(host)
	serving := settled.Serving()
	held := settled.Certificate.ID
	certifying := d.certification(host, &settled)

	if err := certifying.certify(ctx, host, report); err != nil {
		return false, err
	}
	if d.state.Ready(host, d.settle.kind) && settled.Certificate.ID == held {
		return false, certifying.discardSuperseded(ctx, report)
	}

	report.Say(fmt.Sprintf("Binding %s to the %s edge", host, d.settle.kind))
	if err := d.stack.BindDomain(ctx, edge.DomainBinding{Hostname: host, Certificate: settled.Certificate.ID}); err != nil {
		return true, err
	}
	if err := d.checkpoint(ctx); err != nil {
		return true, err
	}
	if err := certifying.discardSuperseded(ctx, report); err != nil {
		return true, err
	}

	records, err := d.settle.recordsFor(d.stack.State(), host)
	if err != nil {
		return true, err
	}
	written, err := d.settle.write(ctx, records,
		fmt.Sprintf("Point %s at the %s edge", host, d.settle.kind), report.Say)
	settled.Written, settled.Owed = written.Written, written.Owed
	d.state.Settle(host, settled)
	if cerr := d.checkpoint(ctx); cerr != nil {
		return true, errors.Join(err, cerr)
	}
	if err != nil {
		return true, err
	}

	probe, err := d.settle.await(ctx, host, report.Say)
	settled.Probe = probe
	d.state.Settle(host, settled)
	if cerr := d.checkpoint(ctx); cerr != nil {
		return true, errors.Join(err, cerr)
	}
	if err != nil {
		return true, err
	}
	report.Say(fmt.Sprintf("%s is served by the %s edge", host, d.settle.kind))
	return true, d.retire(ctx, host, serving, report)
}

func (d *hostnames) certification(host string, settled *Settled) certification {
	return certification{
		provider: d.provider,
		settle:   d.settle,
		settled:  settled,
		uses:     func(id string) bool { return d.state.Uses(id) },
		persist: func(ctx context.Context) error {
			d.state.Settle(host, *settled)
			return d.checkpoint(ctx)
		},
	}
}

func (d *hostnames) retire(ctx context.Context, host string, serving edge.Kind, report Reporter) error {
	if serving == "" || serving == d.settle.kind {
		return nil
	}
	stack, err := d.on(serving)
	if err != nil {
		return err
	}
	report.Say(fmt.Sprintf("Unbinding %s from the %s edge it moved off", host, serving))
	if err := stack.UnbindDomain(ctx, host); err != nil {
		return err
	}
	report.Say(fmt.Sprintf("%s answers on both edges until resolvers drop the record they hold: %s",
		host, flipWindow(d.settle.writer)))
	return nil
}

func (h *handlers) RemoveHostname(ctx context.Context, req *contractv1.HostnameRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	return streamed(ctx, stream, naming.UnitEdge, edgeUnitTitle, progressv1.Phase_PHASE_DELETING, func(_ *eventSender, report Reporter) error {
		session, err := h.hostnames(ctx, req)
		if err != nil {
			return err
		}
		return session.remove(ctx, report)
	})
}

func (d *hostnames) remove(ctx context.Context, report Reporter) error {
	targets, err := d.removeTargets()
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		if len(d.state.Hosts) == 0 {
			report.Say("Nothing to remove: this project serves no production hostname")
			return nil
		}
		report.Say("Nothing to remove: every hostname this project serves is still declared in its config")
		return nil
	}
	for _, host := range targets {
		report.Say(fmt.Sprintf("Unbinding %s from the %s edge", host, d.settle.kind))
		if err := d.stack.UnbindDomain(ctx, host); err != nil {
			return err
		}
		settled := d.state.Host(host)
		if err := d.settle.release(ctx, settled.Written, report.Say); err != nil {
			return err
		}
		d.state.Forget(host)
		if err := d.checkpoint(ctx); err != nil {
			return err
		}
		for _, cert := range settled.certificates() {
			if d.state.Uses(cert.ID) {
				continue
			}
			if err := retireCertificate(ctx, d.provider, d.settle, cert, Certificate{}, report); err != nil {
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
			return nil, Refuse(CodeInvalid, "this project serves no %q: it serves %s", d.host, provisionedList(provisioned))
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

func (h *handlers) GetHostnameStatus(ctx context.Context, req *contractv1.HostnameRequest) (*contractv1.GetHostnameStatusResponse, error) {
	session, err := h.hostnames(ctx, req)
	if err != nil {
		return nil, RefusalError(err)
	}
	resp, err := session.status(ctx)
	if err != nil {
		return nil, RefusalError(err)
	}
	return resp, nil
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
	hosts := slices.Clone(d.configured)
	for _, host := range d.state.Hostnames() {
		if !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	slices.Sort(hosts)
	return hosts
}

func (d *hostnames) statusOf(ctx context.Context, host string) (*contractv1.ProductionHostname, error) {
	settled := d.state.Host(host)
	held := d.stack.State()
	bound := edge.Pointable(edge.TargetOf(d.settle.kind, d.settle.unbound, held), held.Bound, host)

	var owed []edge.Record
	if bound {
		wanted, err := d.settle.recordsFor(held, host)
		if err != nil {
			return nil, err
		}
		owed = edge.Unwritten(wanted, settled.Written)
	}

	probe := settled.Probe
	if bound {
		probe = d.probe(ctx, host)
	}
	health, err := inspectCertificate(ctx, d.provider, d.settle.kind, host, settled.Certificate)
	if err != nil {
		return nil, err
	}
	row := &contractv1.ProductionHostname{
		Hostname:       host,
		Declared:       slices.Contains(d.configured, host),
		Certificate:    certificateState(settled, probe, owed, health.Status),
		RenewalStatus:  health.Renewal,
		ExpiresAt:      health.ExpiresAt,
		ExpiringSoon:   health.ExpiringSoon,
		ServingPointer: servingPointer(probe, d.settle.kind),
	}
	row.Pending = d.pendingOn(host, settled.Certificate, health, bound, probe)
	row.Ready = row.GetPending() == ""
	return row, nil
}

func (d *hostnames) probe(ctx context.Context, host string) Probe {
	once := d.settle
	once.attempts = 1
	probe, err := once.await(ctx, host, func(string) {})
	if err != nil {
		probe.OK = false
	}
	return probe
}

func (d *hostnames) pendingOn(host string, cert Certificate, health CertificateHealth, bound bool, probe Probe) string {
	switch {
	case !slices.Contains(d.configured, host):
		return fmt.Sprintf("this project no longer declares %s; `ocel domain rm` gives it back", host)
	case health.Terminates && !cert.Held():
		return fmt.Sprintf("no certificate covers %s yet; run `ocel domain add`", host)
	case health.Terminates && !health.Issued:
		return fmt.Sprintf("certificate %s is %s, not issued", cert.ID, certificateStatusWord(health.Status))
	case health.Terminates && !health.Covers:
		return fmt.Sprintf("certificate %s covers %s, which does not include %s",
			cert.ID, strings.Join(health.Domains, ", "), host)
	case !bound:
		return fmt.Sprintf("%s is not bound to the %s edge yet; run `ocel domain add`", host, d.settle.kind)
	case !probe.OK:
		return fmt.Sprintf("%s does not answer as the %s edge yet", host, d.settle.kind)
	}
	return ""
}

func servingPointer(probe Probe, kind edge.Kind) string {
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

func certificateState(settled Settled, probe Probe, owed []edge.Record, status string) *contractv1.CertificateState {
	return &contractv1.CertificateState{
		CertificateId:     settled.Certificate.ID,
		CertificateStatus: status,
		RecordsWritten:    recordLines(settled.WrittenRecords()),
		RecordsOwed:       recordLines(append(settled.OwedRecords(), owed...)),
		LastProbeAt:       probe.At,
		LastProbeOk:       probe.OK,
		LastProbeEdge:     string(probe.Edge),
	}
}
