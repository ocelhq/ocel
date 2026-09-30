package domain

import (
	"fmt"
	"io"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func renderBoundHostnames(out io.Writer, resp *contractv1.GetHostnameStatusResponse, configName string) {
	if len(resp.GetHostnames()) == 0 {
		fmt.Fprintf(out, "This project declares no domains.production in %s and serves none.\n", configName)
		fmt.Fprintln(out, "  → declare one and run `ocel domain add`; `ocel domain ls --preview` shows the domain every project's previews share")
		return
	}
	for _, host := range resp.GetHostnames() {
		fmt.Fprintf(out, "%-8s %s", domainHostState(host), host.GetHostname())
		if pointer := host.GetServingPointer(); pointer != "" {
			fmt.Fprintf(out, "  → %s", pointer)
		}
		fmt.Fprintln(out)
		if pending := host.GetPending(); pending != "" {
			fmt.Fprintf(out, "         %s\n", pending)
		}
	}
}

func renderGlobalDomain(out io.Writer, resp *contractv1.GetPreviewWildcardResponse) {
	domain := resp.GetWildcard()
	if domain.GetBaseDomain() == "" {
		fmt.Fprintln(out, "No global preview domain is configured.")
		fmt.Fprintln(out, "  → run `ocel domain use '*.preview.example.com' --preview` to serve every project's previews on one wildcard")
		return
	}

	fmt.Fprintf(out, "Global preview domain  %s\n", wildcardOf(domain.GetBaseDomain()))
	if scope := domain.GetEdgeScope(); scope != "" {
		fmt.Fprintf(out, "  Edge account         %s\n", scope)
	}
	route := "installed"
	if !domain.GetRouteInstalled() {
		route = "MISSING — run `ocel domain use '" + wildcardOf(domain.GetBaseDomain()) + "' --preview` to reinstall it"
	}
	fmt.Fprintf(out, "  Wildcard route       %s\n", route)
	cert := domain.GetCertificate()
	if id := cert.GetCertificateId(); id != "" {
		fmt.Fprintf(out, "  Certificate          %s  %s\n", cert.GetCertificateStatus(), id)
		fmt.Fprintf(out, "  Renewal              %s\n", wildcardRenewal(domain))
	}
	renderCertificateRecords(out, cert)
	fmt.Fprintf(out, "  Last probe           %s\n", lastProbe(cert, "never — run `ocel domain use '"+wildcardOf(domain.GetBaseDomain())+"' --preview` to check the edge answers"))
	renderGlobalDomainProjects(out, resp.GetProjects())
}

func renderDomainRecords(out io.Writer, name string, records []string, empty string) {
	if len(records) == 0 {
		fmt.Fprintf(out, "  %-20s %s\n", name, empty)
		return
	}
	for i, rec := range records {
		fmt.Fprintf(out, "  %-20s %s\n", label(i, name), rec)
	}
}

func label(i int, name string) string {
	if i == 0 {
		return name
	}
	return ""
}

func lastProbe(cert *contractv1.CertificateState, never string) string {
	if cert.GetLastProbeAt() == 0 {
		return never
	}
	at := epochRFC3339(cert.GetLastProbeAt())
	if !cert.GetLastProbeOk() {
		return at + "  FAILED — nothing answered for this project"
	}
	return at + "  answered"
}

func renderCertificateRecords(out io.Writer, cert *contractv1.CertificateState) {
	renderDomainRecords(out, "Records ocel wrote", cert.GetRecordsWritten(), "none — nothing here writes DNS")
	renderDomainRecords(out, "Records you own", cert.GetManualRecords(), "none outstanding")
}

func renderGlobalDomainProjects(out io.Writer, projects []string) {
	if len(projects) == 0 {
		fmt.Fprintln(out, "No project is bootstrapped into the preview tier yet.")
		return
	}
	fmt.Fprintf(out, "Projects served (%d):\n", len(projects))
	for _, p := range projects {
		fmt.Fprintf(out, "  • %s\n", p)
	}
}

func renderDomainStatus(out io.Writer, resp *contractv1.GetHostnameStatusResponse, configName string) {
	if len(resp.GetHostnames()) == 0 {
		fmt.Fprintf(out, "This project declares no domains.production in %s, so nothing is served under a hostname of its own.\n", configName)
		return
	}
	if len(resp.GetRecordsWritten()) > 0 || len(resp.GetManualRecords()) > 0 {
		fmt.Fprintln(out, "Certificate validation")
		renderDomainRecords(out, "Records ocel wrote", resp.GetRecordsWritten(), "none — nothing here writes DNS")
		renderDomainRecords(out, "Records you own", resp.GetManualRecords(), "none outstanding")
		fmt.Fprintln(out)
	}
	for i, host := range resp.GetHostnames() {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "%s  %s\n", host.GetHostname(), domainHostState(host))
		if !host.GetDeclared() {
			fmt.Fprintf(out, "  %-20s no — %s no longer declares it\n", "Declared", configName)
		}
		cert := host.GetCertificate()
		if id := cert.GetCertificateId(); id != "" {
			fmt.Fprintf(out, "  %-20s %s  %s\n", "Certificate", cert.GetCertificateStatus(), id)
			fmt.Fprintf(out, "  %-20s %s\n", "Renewal", domainRenewal(host))
		}
		renderCertificateRecords(out, cert)
		fmt.Fprintf(out, "  %-20s %s\n", "Last probe", lastProbe(cert, "never — run `ocel domain add` to bind it and check the edge answers"))
		if pointer := host.GetServingPointer(); pointer != "" {
			fmt.Fprintf(out, "  %-20s %s\n", "Served by", pointer)
		}
		if pending := host.GetPending(); pending != "" {
			fmt.Fprintf(out, "  %-20s %s\n", "Outstanding", pending)
		}
	}
}

func domainHostState(host *contractv1.ProductionHostname) string {
	if host.GetReady() {
		return "READY"
	}
	return "PENDING"
}

func wildcardRenewal(domain *contractv1.PreviewWildcard) string {
	return renewalLine(domain.GetRenewalStatus(), domain.GetExpiresAt(), domain.GetExpiringSoon())
}

func domainRenewal(host *contractv1.ProductionHostname) string {
	return renewalLine(host.GetRenewalStatus(), host.GetExpiresAt(), host.GetExpiringSoon())
}

func renewalLine(status string, expiresAt int64, soon bool) string {
	expiry := "no expiry reported"
	if expiresAt != 0 {
		expiry = "expires " + epochRFC3339(expiresAt)
	}
	if status == "" {
		status = "not reported"
	}
	if soon {
		return fmt.Sprintf("%s, %s — EXPIRING SOON", expiry, status)
	}
	return fmt.Sprintf("%s, %s", expiry, status)
}
