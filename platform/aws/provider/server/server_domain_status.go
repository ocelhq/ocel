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

func (s *Server) DomainStatus(ctx context.Context, req *deploymentsv1.DomainStatusRequest) (*deploymentsv1.DomainStatusResponse, error) {
	session, err := s.domainSession(ctx, domainRequest{
		options:     req.GetOptions(),
		slug:        req.GetSlug(),
		edgeKind:    req.GetEdgeKind(),
		dns:         req.GetDns(),
		configured:  req.GetConfigured(),
		certificate: true,
	})
	if err != nil {
		return nil, err
	}
	return session.status(ctx)
}

func (d *domainSession) status(ctx context.Context) (*deploymentsv1.DomainStatusResponse, error) {
	lookup := newCertLookup(d.issuer, d.recorded, d.pins)
	resp := &deploymentsv1.DomainStatusResponse{
		Ready:          len(d.configured) > 0,
		RecordsWritten: recordList(d.recorded.Written),
		RecordsOwed:    recordList(d.recorded.Owed),
	}
	for _, host := range d.statusHosts() {
		row, err := d.statusOf(ctx, lookup, host)
		if err != nil {
			return nil, err
		}
		resp.Hosts = append(resp.Hosts, row)
		if row.GetDeclared() && !row.GetReady() {
			resp.Ready = false
		}
	}
	return resp, nil
}

func (d *domainSession) statusHosts() []string {
	hosts := slices.Clone(d.configured)
	for _, host := range d.recorded.Hostnames() {
		if !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	slices.Sort(hosts)
	return hosts
}

func (d *domainSession) statusOf(ctx context.Context, lookup *certLookup, host string) (*deploymentsv1.DomainHost, error) {
	provisioned := d.recorded.Host(host)
	row := &deploymentsv1.DomainHost{
		Hostname: host,
		Declared: slices.Contains(d.configured, host),
	}

	cert, err := lookup.of(ctx, host)
	if err != nil {
		return nil, err
	}
	row.CertificateId, row.CertificateStatus = cert.ARN, cert.Status
	row.RenewalStatus = cert.Renewal
	if !cert.NotAfter.IsZero() {
		row.ExpiresAt = cert.NotAfter.Unix()
		row.ExpiringSoon = cert.ExpiringSoon(d.prober.Clock())
	}

	state := d.stack.State()
	target := edge.TargetFor(d.kind, state)
	boundHosts := edge.BoundDomains(state)
	bound := slices.Contains(boundHosts, host)
	row.RecordsWritten = recordList(provisioned.Written)
	if edge.Pointable(target, boundHosts, host) {
		wanted, err := edge.RecordsFor(target, []string{host})
		if err != nil {
			return nil, err
		}
		row.RecordsOwed = recordList(edge.Unwritten(wanted, provisioned.Written))
	}

	var probe certs.Probe
	if bound {
		probe = d.refreshProbe(ctx, host)
		if !probe.At.IsZero() {
			row.LastProbeAt = probe.At.Unix()
			row.LastProbeOk = probe.OK
			row.LastProbeEdge = string(probe.Edge)
		}
	}
	row.ServingPointer = d.pointer(probe)
	row.Pending = d.pendingOn(host, cert, bound, probe)
	row.Ready = row.GetPending() == ""
	return row, nil
}

func (d *domainSession) refreshProbe(ctx context.Context, host string) certs.Probe {
	once := d.prober
	once.Attempts = 1
	probe, err := once.Await(ctx, host, d.kind, nil, func(string) {})
	if err != nil {
		probe.OK = false
	}
	return probe
}

func (d *domainSession) pointer(probe certs.Probe) string {
	if probe.OK && probe.Edge != "" {
		return string(probe.Edge)
	}
	return string(d.kind)
}

func (d *domainSession) pendingOn(host string, cert certs.Certificate, bound bool, probe certs.Probe) string {
	if !slices.Contains(d.configured, host) {
		return fmt.Sprintf("this project no longer declares %s; `ocel domain rm` gives it back", host)
	}
	if d.issuer.API != nil {
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
		return fmt.Sprintf("%s is not bound to the %s edge yet; run `ocel domain add`", host, d.kind)
	}
	if !probe.OK {
		return fmt.Sprintf("%s does not answer as the %s edge yet", host, d.kind)
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
