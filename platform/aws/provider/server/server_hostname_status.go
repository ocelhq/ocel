package server

import (
	"context"
	"fmt"
	"slices"
	"strings"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (s *Server) GetHostnameStatus(ctx context.Context, req *deploymentsv1.GetHostnameStatusRequest) (*deploymentsv1.GetHostnameStatusResponse, error) {
	session, err := s.hostnameSession(ctx, hostnameRequest{
		slug:        req.GetSlug(),
		edgeKind:    string(requestedEdge(req)),
		dns:         requestedDNS(req),
		configured:  req.GetConfigured(),
		certificate: true,
	})
	if err != nil {
		return nil, err
	}
	return session.status(ctx)
}

func (d *hostnameSession) status(ctx context.Context) (*deploymentsv1.GetHostnameStatusResponse, error) {
	lookup := newCertLookup(d.engine.Issuer, d.recorded, d.pins)
	resp := &deploymentsv1.GetHostnameStatusResponse{
		Ready:          len(d.configured) > 0,
		RecordsWritten: recordList(d.recorded.Validation.Written),
		RecordsOwed:    recordList(d.recorded.Validation.Owed),
	}
	for _, host := range d.statusHosts() {
		row, err := d.statusOf(ctx, lookup, host)
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

func (d *hostnameSession) statusHosts() []string {
	hosts := slices.Clone(d.configured)
	for _, host := range d.recorded.Hostnames() {
		if !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	slices.Sort(hosts)
	return hosts
}

func (d *hostnameSession) statusOf(ctx context.Context, lookup *certLookup, host string) (*deploymentsv1.ProductionHostname, error) {
	provisioned := d.recorded.Host(host)
	row := &deploymentsv1.ProductionHostname{
		Hostname: host,
		Declared: slices.Contains(d.configured, host),
	}

	cert, err := lookup.of(ctx, host)
	if err != nil {
		return nil, err
	}
	row.RenewalStatus = cert.Renewal
	if !cert.NotAfter.IsZero() {
		row.ExpiresAt = cert.NotAfter.Unix()
		row.ExpiringSoon = cert.ExpiringSoon(d.engine.Prober.Clock())
	}

	state := d.stack.State()
	target := edge.TargetOf(d.engine.Kind, d.engine.ServesUnbound, state)
	boundHosts := state.Bound
	bound := slices.Contains(boundHosts, host)
	var owed []string
	if edge.Pointable(target, boundHosts, host) {
		wanted, err := edge.RecordsFor(target, []string{host})
		if err != nil {
			return nil, err
		}
		owed = recordList(edge.Unwritten(wanted, provisioned.Records.Written))
	}

	var probe certs.Probe
	if bound {
		probe = d.refreshProbe(ctx, host)
	}
	row.Certificate = certificateState(cert, probe, recordList(provisioned.Records.Written), owed)
	row.ServingPointer = d.pointer(probe)
	row.Pending = d.pendingOn(host, cert, bound, probe)
	row.Ready = row.GetPending() == ""
	return row, nil
}

func (d *hostnameSession) refreshProbe(ctx context.Context, host string) certs.Probe {
	once := d.engine.Prober
	once.Attempts = 1
	probe, err := once.Await(ctx, host, d.engine.Kind, nil, func(string) {})
	if err != nil {
		probe.OK = false
	}
	return probe
}

func (d *hostnameSession) pointer(probe certs.Probe) string {
	if probe.OK && probe.Edge != "" {
		return string(probe.Edge)
	}
	return string(d.engine.Kind)
}

func (d *hostnameSession) pendingOn(host string, cert certs.Certificate, bound bool, probe certs.Probe) string {
	if !slices.Contains(d.configured, host) {
		return fmt.Sprintf("this project no longer declares %s; `ocel domain rm` gives it back", host)
	}
	if d.engine.Issuer.API != nil {
		switch {
		case cert.ARN == "":
			return fmt.Sprintf("no certificate covers %s yet; run `ocel domain add`", host)
		case !cert.Issued():
			return fmt.Sprintf("certificate %s is %s, not %s", cert.ARN, certStatusWord(cert.Status), strings.ToLower(certs.StatusIssued))
		case !cert.Covers(host):
			return fmt.Sprintf("certificate %s covers %s, which does not include %s", cert.ARN, strings.Join(cert.Domains, ", "), host)
		}
	}
	if !bound {
		return fmt.Sprintf("%s is not bound to the %s edge yet; run `ocel domain add`", host, d.engine.Kind)
	}
	if !probe.OK {
		return fmt.Sprintf("%s does not answer as the %s edge yet", host, d.engine.Kind)
	}
	return ""
}

func certStatusWord(status string) string {
	if status == "" {
		return "in no state ACM reports"
	}
	return strings.ToLower(status)
}

func recordList(records []edge.Record) []string {
	if len(records) == 0 {
		return nil
	}
	out := make([]string, 0, len(records))
	for _, rec := range records {
		if line := rec.String(); !slices.Contains(out, line) {
			out = append(out, line)
		}
	}
	return out
}
