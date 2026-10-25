package domain

import (
	"fmt"
	"io"

	"github.com/ocelhq/ocel/cli/internal/terminal"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func renderBoundHostnames(out io.Writer, palette terminal.Palette, resp *contractv1.GetHostnameStatusResponse, configName string) {
	if len(resp.GetHostnames()) == 0 {
		fmt.Fprintf(out, "No domains.production in %s.\n", configName)
		fmt.Fprintln(out, "  → "+palette.Commands("declare one, then run `ocel domain add`"))
		return
	}
	table := terminal.NewTable(terminal.Column{Head: "HOSTNAME"}, terminal.Column{Head: "STATE"}, terminal.Column{Head: "SERVED BY"})
	for _, host := range resp.GetHostnames() {
		pointer := host.GetServingPointer()
		if pointer == "" {
			pointer = terminal.NoValue
		}
		table.Row(palette.Bold(host.GetHostname()), domainHostState(palette, host), pointer)
		if pending := host.GetPending(); pending != "" {
			table.Span("  " + palette.Muted(pending))
		}
	}
	fmt.Fprint(out, table.Render(palette, ""))
}

func renderGlobalDomain(out io.Writer, palette terminal.Palette, resp *contractv1.GetPreviewWildcardResponse) {
	domain := resp.GetWildcard()
	if domain.GetBaseDomain() == "" {
		fmt.Fprintln(out, "No global preview domain is configured.")
		fmt.Fprintln(out, "  → "+palette.Commands("run `ocel domain use '*.preview.example.com' --preview`"))
		return
	}

	fmt.Fprintf(out, "%s  %s\n", palette.Bold("Global preview domain"), wildcardOf(domain.GetBaseDomain()))
	if scope := domain.GetEdgeScope(); scope != "" {
		field(out, palette, "Edge account", scope)
	}
	route := palette.Success("installed")
	if !domain.GetRouteInstalled() {
		route = palette.FailureBold("MISSING") + palette.Commands(" — run `ocel domain use '"+wildcardOf(domain.GetBaseDomain())+"' --preview` to reinstall it")
	}
	field(out, palette, "Wildcard route", route)
	cert := domain.GetCertificate()
	if id := cert.GetCertificateId(); id != "" {
		field(out, palette, "Certificate", cert.GetCertificateStatus()+"  "+palette.Muted(id))
		field(out, palette, "Renewal", wildcardRenewal(palette, domain))
	}
	renderCertificateRecords(out, palette, cert)
	field(out, palette, "Last probe", lastProbe(palette, cert, "never — run `ocel domain use '"+wildcardOf(domain.GetBaseDomain())+"' --preview` to check the edge answers"))
	renderGlobalDomainProjects(out, palette, resp.GetProjects())
}

func field(out io.Writer, palette terminal.Palette, name, value string) {
	fmt.Fprintf(out, "  %s %s\n", palette.Muted(fmt.Sprintf("%-20s", name)), value)
}

func renderDomainRecords(out io.Writer, palette terminal.Palette, name string, records []string, empty string) {
	if len(records) == 0 {
		field(out, palette, name, palette.Muted(empty))
		return
	}
	for i, rec := range records {
		field(out, palette, label(i, name), rec)
	}
}

func label(i int, name string) string {
	if i == 0 {
		return name
	}
	return ""
}

func lastProbe(palette terminal.Palette, cert *contractv1.CertificateState, never string) string {
	if cert.GetLastProbeAt() == 0 {
		return palette.Commands(never)
	}
	at := terminal.EpochRFC3339(cert.GetLastProbeAt())
	if !cert.GetLastProbeOk() {
		return at + "  " + palette.FailureBold("FAILED") + " — nothing answered for this project"
	}
	return at + "  " + palette.Success("answered")
}

func renderCertificateRecords(out io.Writer, palette terminal.Palette, cert *contractv1.CertificateState) {
	renderDomainRecords(out, palette, "Records ocel wrote", cert.GetRecordsWritten(), "none — nothing here writes DNS")
	renderDomainRecords(out, palette, "Records you own", cert.GetManualRecords(), "none outstanding")
}

func renderGlobalDomainProjects(out io.Writer, palette terminal.Palette, projects []string) {
	if len(projects) == 0 {
		fmt.Fprintln(out, "No project is bootstrapped into the preview tier yet.")
		return
	}
	fmt.Fprintf(out, "\n%s\n", palette.Heading(fmt.Sprintf("Projects served (%d)", len(projects))))
	for _, p := range projects {
		fmt.Fprintf(out, "  • %s\n", p)
	}
}

func renderDomainStatus(out io.Writer, palette terminal.Palette, resp *contractv1.GetHostnameStatusResponse, configName string) {
	if len(resp.GetHostnames()) == 0 {
		fmt.Fprintf(out, "No domains.production in %s.\n", configName)
		return
	}
	if len(resp.GetRecordsWritten()) > 0 || len(resp.GetManualRecords()) > 0 {
		fmt.Fprintln(out, palette.Bold("Certificate validation"))
		renderDomainRecords(out, palette, "Records ocel wrote", resp.GetRecordsWritten(), "none — nothing here writes DNS")
		renderDomainRecords(out, palette, "Records you own", resp.GetManualRecords(), "none outstanding")
		fmt.Fprintln(out)
	}
	for i, host := range resp.GetHostnames() {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "%s  %s\n", palette.Bold(host.GetHostname()), domainHostState(palette, host))
		if !host.GetDeclared() {
			field(out, palette, "Declared", palette.Warning("no")+" — "+configName+" no longer declares it")
		}
		cert := host.GetCertificate()
		if id := cert.GetCertificateId(); id != "" {
			field(out, palette, "Certificate", cert.GetCertificateStatus()+"  "+palette.Muted(id))
			field(out, palette, "Renewal", domainRenewal(palette, host))
		}
		renderCertificateRecords(out, palette, cert)
		field(out, palette, "Last probe", lastProbe(palette, cert, "never — run `ocel domain add`"))
		if pointer := host.GetServingPointer(); pointer != "" {
			field(out, palette, "Served by", pointer)
		}
		if pending := host.GetPending(); pending != "" {
			field(out, palette, "Outstanding", palette.Warning(pending))
		}
	}
}

func domainHostState(palette terminal.Palette, host *contractv1.ProductionHostname) string {
	if host.GetReady() {
		return palette.Health(terminal.Healthy, "READY")
	}
	return palette.Health(terminal.Pending, "PENDING")
}

func wildcardRenewal(palette terminal.Palette, domain *contractv1.PreviewWildcard) string {
	return renewalLine(palette, domain.GetRenewalStatus(), domain.GetExpiresAt(), domain.GetExpiringSoon())
}

func domainRenewal(palette terminal.Palette, host *contractv1.ProductionHostname) string {
	return renewalLine(palette, host.GetRenewalStatus(), host.GetExpiresAt(), host.GetExpiringSoon())
}

func renewalLine(palette terminal.Palette, status string, expiresAt int64, soon bool) string {
	expiry := "no expiry reported"
	if expiresAt != 0 {
		expiry = "expires " + terminal.EpochRFC3339(expiresAt)
	}
	if status == "" {
		status = "not reported"
	}
	if soon {
		return fmt.Sprintf("%s, %s — %s", expiry, status, palette.WarningBold("EXPIRING SOON"))
	}
	return fmt.Sprintf("%s, %s", expiry, status)
}
