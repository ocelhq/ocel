package vps_test

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func rendered(t *testing.T, purpose edge.CredentialPurpose) edge.CredentialDocument {
	t.Helper()
	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10", User: "deployer"}})
	document, err := p.Credentials().Permissions(purpose, environment.TierProduction)
	if err != nil {
		t.Fatalf("Permissions(%q) = %v", purpose, err)
	}
	if strings.TrimSpace(document.Document) == "" {
		t.Fatalf("Permissions(%q) rendered nothing", purpose)
	}
	return document
}

func TestBothDocumentsAreBareStringsAShellCanPipe(t *testing.T) {
	t.Parallel()

	for _, purpose := range []edge.CredentialPurpose{edge.PurposeBootstrap, edge.PurposeDeploy} {
		if heading := rendered(t, purpose).Heading; heading != "" {
			t.Errorf("the %s document is headed %q, and a host has one credential set, not several", purpose, heading)
		}
	}
}

func TestTheBootstrapDocumentNamesEveryRequirementPreflightChecks(t *testing.T) {
	t.Parallel()

	document := rendered(t, edge.PurposeBootstrap).Document
	for _, need := range ssh.Requirements() {
		for _, want := range []string{need.Name, need.Detail} {
			if !strings.Contains(document, want) {
				t.Errorf("the bootstrap document does not include %q, so it says less than preflight demands:\n%s", want, document)
			}
		}
	}
}

func TestTheBootstrapDocumentIncludesTheSudoersFragmentTheLoginNeeds(t *testing.T) {
	t.Parallel()

	document := rendered(t, edge.PurposeBootstrap).Document
	for _, want := range []string{"/etc/sudoers.d/", "NOPASSWD:", "deployer ALL="} {
		if !strings.Contains(document, want) {
			t.Errorf("the bootstrap document does not include %q, and it is what a human is meant to paste:\n%s", want, document)
		}
	}
}

func TestTheBootstrapDocumentNamesTheLoginItCannotResolve(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Alias: "prod-box"}})
	document, err := p.Credentials().Permissions(edge.PurposeBootstrap, environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(document.Document, "<login>") {
		t.Errorf("the bootstrap document for a host named by an ssh_config alias reads:\n%s\nwant the sudoers line to name whatever login that alias resolves to", document.Document)
	}
}

func TestTheDeployDocumentNamesEveryGrantTheApplyMakes(t *testing.T) {
	t.Parallel()

	document := rendered(t, edge.PurposeDeploy).Document
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		for _, grant := range host.Grants(tier, front.Proxy{}) {
			for _, want := range []string{grant.Name, grant.Detail} {
				if !strings.Contains(document, want) {
					t.Errorf("the deploy document does not include %q, so it claims less than a bootstrap hands out:\n%s", want, document)
				}
			}
		}
	}
}

func TestEveryPathTheDeployLoginOwnsIsInTheDeployDocument(t *testing.T) {
	t.Parallel()

	document := rendered(t, edge.PurposeDeploy).Document
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		for _, item := range host.Items(tier, nil, ssh.AMD64, front.Proxy{}) {
			if item.Owner != "ocel-deploy" || item.Kind == "linux:user" {
				continue
			}
			if !strings.Contains(document, item.Name) {
				t.Errorf("apply hands %s to the deploy login and the document never mentions it:\n%s", item.Name, document)
			}
		}
	}
}

func describedGroups(t *testing.T, tier environment.Tier) string {
	t.Helper()
	for _, item := range host.Items(tier, nil, ssh.AMD64, front.Proxy{}) {
		if item.Kind != "linux:user" {
			continue
		}
		for _, line := range strings.Split(string(item.Content), "\n") {
			if groups, named := strings.CutPrefix(line, "groups="); named {
				return groups
			}
		}
		t.Fatalf("the deploy login is described as %q, which names no group either way", item.Content)
	}
	t.Fatal("nothing in the item set describes the deploy login")
	return ""
}

func TestTheDeployDocumentSaysTheDeployLoginReachesDockerOnlyThroughTheCheck(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	if groups := describedGroups(t, tier); groups != "" {
		t.Fatalf("apply writes the deploy login into %q, and the docker group is root on the machine under another name", groups)
	}
	var claim host.Grant
	for _, grant := range host.Grants(tier, front.Proxy{}) {
		if strings.HasPrefix(grant.Name, "runs docker only through ") {
			claim = grant
		}
	}
	if claim.Name == "" {
		t.Fatal("the document claims nothing about how the deploy login reaches docker")
	}
	if !strings.Contains(claim.Detail, "in no group that reaches the docker socket") {
		t.Errorf("the document describes the deploy login's docker as:\n%s\nand never says it is kept off the socket", claim.Detail)
	}
	if document := rendered(t, edge.PurposeDeploy).Document; !strings.Contains(document, claim.Detail) {
		t.Errorf("the document does not include the docker grant word for word:\n%s", document)
	}
}

func TestTheDeployDocumentIncludesEverySudoersLineBootstrapWrites(t *testing.T) {
	t.Parallel()

	document := rendered(t, edge.PurposeDeploy).Document
	sealing := false
	for _, item := range host.Items(environment.TierProduction, nil, ssh.AMD64, front.Proxy{}) {
		if !strings.HasPrefix(item.Name, "/etc/sudoers.d/") {
			continue
		}
		if !strings.Contains(document, strings.TrimSpace(string(item.Content))) {
			t.Errorf("a bootstrap writes %s and the document never prints the line it contains:\n%s", item.Name, document)
		}
		sealing = sealing || strings.Contains(string(item.Content), boxapi.LiveBinary+" "+boxapi.SealCommand)
	}
	if !sealing {
		t.Fatal("nothing in the item set whitelists the seal helper, so the deploy login seals nothing")
	}
}

func TestTheDeployDocumentNamesTheCaddyReloadLineOnlyBehindACaddyService(t *testing.T) {
	t.Parallel()

	for proxy, want := range map[*front.Proxy]bool{
		nil: false,
		{Caddy: &front.Caddy{Directory: "/etc/caddy/ocel.d"}}:                                     true,
		{Caddy: &front.Caddy{Directory: "/etc/caddy/ocel.d", Container: "caddy", Network: "web"}}: false,
		{Caddy: &front.Caddy{Preset: "coolify"}}:                                                  false,
	} {
		p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10", User: "deployer"}, Proxy: proxy})
		document, err := p.Credentials().Permissions(edge.PurposeDeploy, environment.TierProduction)
		if err != nil {
			t.Fatalf("Permissions(deploy) = %v", err)
		}
		if named := strings.Contains(document.Document, "/etc/sudoers.d/ocel-caddy-reload"); named != want {
			t.Errorf("behind %+v the deploy document names /etc/sudoers.d/ocel-caddy-reload = %v, want %v: bootstrap writes it only for a Caddy that runs as caddy.service", proxy, named, want)
		}
	}
}

func TestTheDeployDocumentSaysTheSealKeyIsNotTheDeployLoginsToRead(t *testing.T) {
	t.Parallel()

	document := rendered(t, edge.PurposeDeploy).Document
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		if !strings.Contains(document, host.SealKeyPath(tier)) {
			t.Errorf("the document says nothing about %s, and a login that opens values should know what it never reads:\n%s",
				host.SealKeyPath(tier), document)
		}
	}
}

func TestTheDeployDocumentSaysTheKeyIsNeverRotated(t *testing.T) {
	t.Parallel()

	document := rendered(t, edge.PurposeDeploy).Document
	if !strings.Contains(document, "rotat") {
		t.Errorf("the document never says whether a seal key can be rotated, and a key nothing rotates is a fact a user needs before they seal to it:\n%s", document)
	}
}

func TestCredentialsThatAreNeitherPurposeAreRefused(t *testing.T) {
	t.Parallel()

	p := vps.NewProvider(vps.Options{SSH: vps.Target{Host: "203.0.113.10"}})
	if _, err := p.Credentials().Permissions("admin", environment.TierProduction); err == nil {
		t.Error("Permissions() rendered a document for a purpose this provider has no credentials for")
	}
}
