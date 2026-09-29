package domain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func productionConfig(dns string, hosts ...string) string {
	quoted := make([]string, 0, len(hosts))
	for _, host := range hosts {
		quoted = append(quoted, `"`+host+`"`)
	}
	config := "export default {\n  slug: \"test-app\",\n  provider: { fake: {} },\n  domains: { production: [" + strings.Join(quoted, ", ") + "] },\n"
	if dns != "" {
		config += "  dns: \"" + dns + "\",\n"
	}
	return config + "};\n"
}

func deployedProject(t *testing.T, config string) clitest.FakeProject {
	t.Helper()
	project := clitest.SetUpProject(t)
	clitest.WriteFile(t, filepath.Join(project.Root, "ocel.config.ts"), config)
	clitest.RecordPromotions(t, project, "p1")
	return project
}

func attach(t *testing.T, project clitest.FakeProject) {
	t.Helper()
	invocation := newTestInvocation()
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stdout)
	if err := runDomainAdd(context.Background(), invocation, project.Root, "", &stdout, &stderr); err != nil {
		t.Fatalf("attach the production hostnames: %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
}

func answers(t *testing.T, project clitest.FakeProject, hostname string) bool {
	t.Helper()
	serving, err := project.Provider.Liveness().ServingRouter(context.Background(), hostname)
	if err != nil {
		t.Fatalf("probe %s: %v", hostname, err)
	}
	return serving != ""
}

func requireValidation(project clitest.FakeProject, hostname string) {
	project.Provider.RequireValidationRecords(edge.Record{Name: "_ocel." + hostname, Type: "CNAME", Value: "_target.validations.fake.invalid"})
}

func recordWildcard(t *testing.T, project clitest.FakeProject, wildcard stackrecords.Wildcard) {
	t.Helper()
	ctx := context.Background()
	name := stackrecords.WildcardKey(environment.TierPreview)
	recorded, err := keyvalue.ReadOrEmpty(ctx, project.Provider.KeyValues(), name)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Value, err = json.Marshal(wildcard); err != nil {
		t.Fatal(err)
	}
	if _, err := project.Provider.KeyValues().Write(ctx, recorded); err != nil {
		t.Fatal(err)
	}
}

func TestDomainListWithPreviewShowsTheGlobalDomainAndTheProjectsOnIt(t *testing.T) {
	t.Run("ls names the domain and the projects served on it", func(t *testing.T) {
		project := previewProject(t)
		useWildcard(t, project)
		servedOnWildcard(t, project, "shop")
		servedOnWildcard(t, project, "blog")
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainList(context.Background(), invocation, project.Root, domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"*.preview.acme.com", "fake-account", "Wildcard route       installed", "Projects served (2)", "shop", "blog"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
	})

	t.Run("ls shows the certificate, the records and the last probe", func(t *testing.T) {
		project := previewProject(t)
		writeZonedPreviewConfig(t, project.Root)
		requireValidation(project, "preview.acme.com")
		project.Provider.ReportCertificate(provider.CertificateHealth{
			Terminates:   true,
			Issued:       true,
			Covers:       true,
			Status:       "ISSUED",
			Renewal:      "you placed it on this box and you renew it",
			ExpiresAt:    1755500000,
			ExpiringSoon: true,
		})
		useWildcard(t, project)
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainList(context.Background(), invocation, project.Root, domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"Certificate          ISSUED  issued-for-*.preview.acme.com",
			"Renewal              expires 2025-08-18T06:53:20Z, you placed it on this box and you renew it — EXPIRING SOON",
			"Records ocel wrote   *.preview.acme.com CNAME preview.relay.fake.invalid",
			"_ocel.preview.acme.com CNAME _target.validations.fake.invalid",
			"Records you own      none outstanding",
			"answered",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
	})

	t.Run("ls names the records the user has to write when nothing writes DNS", func(t *testing.T) {
		project := previewProject(t)
		requireValidation(project, "preview.acme.com")
		useWildcard(t, project)
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainList(context.Background(), invocation, project.Root, domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"Records ocel wrote   none — nothing here writes DNS",
			"Records you own      *.preview.acme.com CNAME preview.relay.fake.invalid",
			"_ocel.preview.acme.com CNAME _target.validations.fake.invalid",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
	})

	t.Run("ls says an unprobed domain has never been probed", func(t *testing.T) {
		project := previewProject(t)
		recordWildcard(t, project, stackrecords.Wildcard{BaseDomain: "preview.acme.com", Edge: fake.KindRelay})
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainList(context.Background(), invocation, project.Root, domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "Last probe           never") {
			t.Errorf("stdout = %q, want it to say the domain has never been probed", out)
		}
		if strings.Contains(out, "Certificate") {
			t.Errorf("stdout = %q, want no certificate line when none is recorded", out)
		}
		for _, want := range []string{"Records ocel wrote   none", "Records you own      none outstanding"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
	})

	t.Run("ls says so when no global domain is configured", func(t *testing.T) {
		project := previewProject(t)
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stderr)
		if err := runDomainList(context.Background(), invocation, project.Root, domainOptions{preview: true}, &stdout, &stderr); err != nil {
			t.Fatalf("runDomainList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"No global preview domain is configured", "ocel domain use"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
	})
}

func TestDomainAddProvisionsOnlyTheHostnamesTheConfigDeclares(t *testing.T) {
	t.Run("add renders every step over the configured hosts", func(t *testing.T) {
		project := deployedProject(t, productionConfig("zone", "shop.app.com", "www.app.com"))
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDomainAdd(context.Background(), invocation, project.Root, "", &stdout, &stderr); err != nil {
			t.Fatalf("runDomainAdd err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{
			"Binding shop.app.com to the relay edge",
			"Writing shop.app.com CNAME test-app.relay.fake.invalid",
			"shop.app.com is served through the relay edge",
			"Binding www.app.com to the relay edge",
			"Serving shop.app.com, www.app.com",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		asked := clitest.RequestsTo[*contractv1.HostnameRequest](t, project.Requests, contractv1connect.ProviderServiceAddHostnameProcedure)
		if len(asked) != 1 || asked[0].GetSlug() != "test-app" || asked[0].GetHost() != "" || asked[0].GetEdge().GetDns().GetKind() != "zone" {
			t.Fatalf("the CLI asked %v, want one add of every configured host with the zone dns", asked)
		}
		if configured := configuredNames(asked[0]); !slices.Equal(configured, []string{"shop.app.com", "www.app.com"}) {
			t.Errorf("the CLI sent the configured hosts %v, want shop.app.com and www.app.com", configured)
		}
		for _, host := range []string{"shop.app.com", "www.app.com"} {
			if !answers(t, project, host) {
				t.Errorf("%s does not answer through the edge after the add", host)
			}
		}
	})

	t.Run("add with a host attaches only that one", func(t *testing.T) {
		project := deployedProject(t, productionConfig("", "shop.app.com", "www.app.com"))
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDomainAdd(context.Background(), invocation, project.Root, "www.app.com", &stdout, &stderr); err != nil {
			t.Fatalf("runDomainAdd err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"Point www.app.com at the relay edge — add this record at your DNS provider", "CNAME  www.app.com  test-app.relay.fake.invalid", "Serving www.app.com"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		if strings.Contains(out, "shop.app.com") {
			t.Errorf("stdout = %q, want the host that was not named left out", out)
		}
		if answers(t, project, "shop.app.com") {
			t.Error("shop.app.com answers through the edge, want only the named host attached")
		}
	})

	t.Run("add exits non-zero when the hostname does not answer, naming why", func(t *testing.T) {
		project := deployedProject(t, productionConfig("", "shop.app.com"))
		project.Provider.QueueProbeFailures("shop.app.com", errors.New("shop.app.com refused the connection"))
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDomainAdd(context.Background(), invocation, project.Root, "", &stdout, &stderr)
		if err == nil {
			t.Fatalf("runDomainAdd err = nil, want the failed probe surfaced; stdout=%s", stdout.String())
		}
		if rendered := stdout.String() + stderr.String() + err.Error(); !strings.Contains(rendered, "shop.app.com refused the connection") {
			t.Errorf("rendered output = %q, want it to name why the hostname did not answer", rendered)
		}
	})

	t.Run("add refuses a host the config does not declare", func(t *testing.T) {
		project := deployedProject(t, productionConfig("", "shop.app.com"))
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDomainAdd(context.Background(), invocation, project.Root, "other.app.com", &stdout, &stderr)
		if err == nil {
			t.Fatal("runDomainAdd err = nil, want a refusal: no command edits the config")
		}
		rendered := stdout.String() + stderr.String()
		for _, want := range []string{"domains.production", "shop.app.com", "other.app.com"} {
			if !strings.Contains(rendered, want) {
				t.Errorf("rendered output = %q, want it to contain %q", rendered, want)
			}
		}
		if answers(t, project, "other.app.com") {
			t.Error("other.app.com answers through the edge, want nothing attached")
		}
	})

	t.Run("add refuses a project that declares no production hostname", func(t *testing.T) {
		project := clitest.SetUpProject(t)
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDomainAdd(context.Background(), invocation, project.Root, "", &stdout, &stderr)
		if err == nil {
			t.Fatal("runDomainAdd err = nil, want a refusal with nothing declared")
		}
		if !strings.Contains(err.Error(), "domains.production") {
			t.Errorf("err = %v, want it to name what to declare", err)
		}
	})
}

func configuredNames(req *contractv1.HostnameRequest) []string {
	var names []string
	for _, configured := range req.GetConfigured() {
		names = append(names, configured.GetHostname())
	}
	return names
}

func TestDomainRemoveUnbindsWhatTheConfigNoLongerDeclares(t *testing.T) {
	t.Run("rm sends the configured set so the provider knows what was dropped", func(t *testing.T) {
		project := deployedProject(t, productionConfig("zone", "shop.app.com", "old.app.com"))
		attach(t, project)
		clitest.WriteFile(t, filepath.Join(project.Root, "ocel.config.ts"), productionConfig("zone", "shop.app.com"))
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDomainRemove(context.Background(), invocation, project.Root, "", domainOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDomainRemove err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"Unbinding old.app.com from the relay edge", "Removed every hostname this project no longer declares"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		asked := clitest.RequestsTo[*contractv1.HostnameRequest](t, project.Requests, contractv1connect.ProviderServiceRemoveHostnameProcedure)
		if len(asked) != 1 || asked[0].GetHost() != "" || !slices.Equal(configuredNames(asked[0]), []string{"shop.app.com"}) {
			t.Errorf("the CLI asked %v, want a removal carrying the configured set shop.app.com", asked)
		}
		if answers(t, project, "old.app.com") || !answers(t, project, "shop.app.com") {
			t.Error("want old.app.com unbound and shop.app.com still served")
		}
	})

	t.Run("rm with a host unbinds it", func(t *testing.T) {
		project := deployedProject(t, productionConfig("", "shop.app.com", "old.app.com"))
		attach(t, project)
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDomainRemove(context.Background(), invocation, project.Root, "old.app.com", domainOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDomainRemove err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"Unbinding old.app.com from the relay edge", "Removed old.app.com"} {
			if !strings.Contains(out, want) {
				t.Errorf("stdout = %q, want it to contain %q", out, want)
			}
		}
		if answers(t, project, "old.app.com") {
			t.Error("old.app.com still answers through the edge after rm")
		}
	})

	t.Run("rm refuses without a terminal or --yes and removes nothing", func(t *testing.T) {
		project := deployedProject(t, productionConfig("", "shop.app.com", "old.app.com"))
		attach(t, project)
		invocation := newTestInvocation()

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		err := runDomainRemove(context.Background(), invocation, project.Root, "old.app.com", domainOptions{}, &stdout, &stderr, strings.NewReader(""))
		if err == nil || !strings.Contains(err.Error(), "pass --yes") {
			t.Fatalf("runDomainRemove without a terminal err = %v, want a refusal naming --yes", err)
		}
		if !answers(t, project, "old.app.com") {
			t.Error("old.app.com stopped answering, want nothing unbound without consent")
		}
	})

	t.Run("rm behind a declined confirmation removes nothing", func(t *testing.T) {
		project := deployedProject(t, productionConfig("", "shop.app.com", "old.app.com"))
		attach(t, project)
		invocation := newTestInvocation()
		invocation.StdinIsTerminal = func(io.Reader) bool { return true }

		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(invocation, &stdout)
		if err := runDomainRemove(context.Background(), invocation, project.Root, "old.app.com", domainOptions{}, &stdout, &stderr, strings.NewReader("n\n")); err != nil {
			t.Fatalf("runDomainRemove err = %v; stdout=%s", err, stdout.String())
		}
		out := stdout.String()
		if !strings.Contains(out, "Not confirmed, so this run changes nothing") {
			t.Errorf("stdout = %q, want a declined confirmation to say so", out)
		}
		if strings.Contains(out, "Unbinding") || strings.Contains(out, "Removed old.app.com") {
			t.Errorf("stdout = %q, want nothing removed behind a declined confirmation", out)
		}
		if !answers(t, project, "old.app.com") {
			t.Error("old.app.com stopped answering, want nothing unbound behind a declined confirmation")
		}
	})
}

func TestDomainListListsThisProjectsOwnHostnamesWithoutPreview(t *testing.T) {
	project := deployedProject(t, productionConfig("zone", "shop.app.com"))
	attach(t, project)
	invocation := newTestInvocation()

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stderr)
	if err := runDomainList(context.Background(), invocation, project.Root, domainOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runDomainList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"shop.app.com", "READY"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to name %q: `ocel domain ls` lists what this project has bound", out, want)
		}
	}
}

func TestDomainListReadsStateWhileStatusChecksTheEdgeLive(t *testing.T) {
	project := deployedProject(t, productionConfig("zone", "shop.app.com"))
	invocation := newTestInvocation()

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stderr)
	if err := runDomainList(context.Background(), invocation, project.Root, domainOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runDomainList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	asked := clitest.RequestsTo[*contractv1.HostnameRequest](t, project.Requests, contractv1connect.ProviderServiceGetHostnameStatusProcedure)
	if len(asked) != 1 || asked[0].GetProbe() {
		t.Errorf("`ocel domain ls` asked %v, want it to read state: a probe reaches every declared hostname over the network on a timeout each, so listing waits as long as the project has hostnames", asked)
	}

	stdout.Reset()
	stderr.Reset()
	if err := runDomainStatus(context.Background(), invocation, project.Root, domainOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runDomainStatus err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	asked = clitest.RequestsTo[*contractv1.HostnameRequest](t, project.Requests, contractv1connect.ProviderServiceGetHostnameStatusProcedure)
	if len(asked) != 2 || !asked[1].GetProbe() {
		t.Errorf("`ocel domain status` asked %v, want it to check the edge live: it is the acceptance test for a bind, and a hostname that stopped answering looks served in the record until something asks", asked)
	}
}
