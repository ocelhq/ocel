package vps_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func rendered(t *testing.T, tier providerkit.CredentialTier) edge.CredentialDocument {
	t.Helper()
	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10", User: "deployer"}})
	document, err := p.Credentials().Permissions(tier)
	if err != nil {
		t.Fatalf("Permissions(%q) = %v", tier, err)
	}
	if strings.TrimSpace(document.Document) == "" {
		t.Fatalf("Permissions(%q) rendered nothing", tier)
	}
	return document
}

func TestBothDocumentsAreBareStringsAShellCanPipe(t *testing.T) {
	t.Parallel()

	for _, tier := range []providerkit.CredentialTier{providerkit.TierBootstrap, providerkit.TierDeploy} {
		if heading := rendered(t, tier).Heading; heading != "" {
			t.Errorf("the %s document is headed %q, and a host holds one credential set, not several", tier, heading)
		}
	}
}

func TestTheBootstrapDocumentNamesEveryRequirementPreflightChecks(t *testing.T) {
	t.Parallel()

	document := rendered(t, providerkit.TierBootstrap).Document
	for _, need := range session.Requirements() {
		for _, want := range []string{need.Name, need.Detail} {
			if !strings.Contains(document, want) {
				t.Errorf("the bootstrap document does not carry %q, so it says less than preflight demands:\n%s", want, document)
			}
		}
	}
}

func TestTheBootstrapDocumentCarriesTheSudoersFragmentTheLoginNeeds(t *testing.T) {
	t.Parallel()

	document := rendered(t, providerkit.TierBootstrap).Document
	for _, want := range []string{"/etc/sudoers.d/", "NOPASSWD:", "deployer ALL="} {
		if !strings.Contains(document, want) {
			t.Errorf("the bootstrap document does not carry %q, and it is what a human is meant to paste:\n%s", want, document)
		}
	}
}

func TestTheBootstrapDocumentNamesTheLoginItCannotResolve(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Alias: "prod-box"}})
	document, err := p.Credentials().Permissions(providerkit.TierBootstrap)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(document.Document, "<login>") {
		t.Errorf("the bootstrap document for a host named by an ssh_config alias reads:\n%s\nwant the sudoers line to stand for whatever login that alias resolves to", document.Document)
	}
}

func TestTheDeployDocumentNamesEveryGrantTheApplyMakes(t *testing.T) {
	t.Parallel()

	document := rendered(t, providerkit.TierDeploy).Document
	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		for _, grant := range host.Grants(class) {
			for _, want := range []string{grant.Name, grant.Detail} {
				if !strings.Contains(document, want) {
					t.Errorf("the deploy document does not carry %q, so it claims less than a bootstrap hands out:\n%s", want, document)
				}
			}
		}
	}
}

func TestEveryPathTheDeployLoginOwnsIsInTheDeployDocument(t *testing.T) {
	t.Parallel()

	document := rendered(t, providerkit.TierDeploy).Document
	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		for _, item := range host.Items(class, nil) {
			if item.Owner != "ocel-deploy" || item.Kind == "linux:user" {
				continue
			}
			if !strings.Contains(document, item.Name) {
				t.Errorf("apply hands %s to the deploy login and the document never mentions it:\n%s", item.Name, document)
			}
		}
	}
}

func TestTheDeployDocumentSaysWhatTheDockerGroupIs(t *testing.T) {
	t.Parallel()

	document := strings.ToLower(rendered(t, providerkit.TierDeploy).Document)
	if !strings.Contains(document, "docker") || !strings.Contains(document, "root") {
		t.Errorf("the deploy document does not say that the docker group is root on the machine:\n%s", document)
	}
}

func TestCredentialsThatAreNeitherTierAreRefused(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	if _, err := p.Credentials().Permissions("admin"); err == nil {
		t.Error("Permissions() rendered a document for a tier this provider has no credentials for")
	}
}
