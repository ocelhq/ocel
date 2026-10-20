package commands_test

import (
	"bytes"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

func runOnCloud(t *testing.T, invocation commands.Invocation, root, command string) (string, error) {
	t.Helper()
	target, err := invocation.ResolveTarget(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !target.IsCloud() {
		t.Fatal("the target is not Ocel Cloud")
	}
	var out bytes.Buffer
	clitest.AttachTerminalSink(invocation, &out)
	err = invocation.UploadToCloud(t.Context(), target, command, environmentv1.Tier_TIER_PRODUCTION)
	if closeErr := invocation.Events.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return out.String(), err
}

func TestTheFirstOcelCloudRunLinksAConsoleProjectUploadsTheSourceAndStopsBeforeAJob(t *testing.T) {
	invocation, fake := loggedIn(t)
	root := filepath.Join(t.TempDir(), "shop")
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)
	clitest.WriteFile(t, filepath.Join(root, "index.js"), "console.log('hi')")
	clitest.WriteFile(t, filepath.Join(root, ".env"), "SECRET=1")

	out, err := runOnCloud(t, invocation, root, "ocel deploy")

	var coded *clierror.Error
	if !errors.As(err, &coded) || coded.Code != clierror.CodeCloudUnsupported || !strings.Contains(err.Error(), clitest.FakeCloudSourceID) {
		t.Fatalf("err = %v, want the run stopped before a job, naming the source id", err)
	}
	first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	if !strings.Contains(first, "shop") {
		t.Fatalf("first line = %q, want the identity of the run", first)
	}
	if !strings.Contains(out, "Ocel Cloud") || !strings.Contains(out, "Acme Inc") || !strings.Contains(out, "127.0.0.1") {
		t.Fatalf("output = %q, want the target named: Ocel Cloud, the organization and the console", out)
	}
	if len(fake.Created) != 1 || fake.Created[0].GetSlug() != "shop" {
		t.Fatalf("created %v, want the console project shop", fake.Created)
	}
	link, err := console.ReadLink(root, fake.ConsoleURL)
	if err != nil || link == nil || link.OrganizationID != "org_1" || link.ProjectName != "shop" {
		t.Fatalf("link = %+v, err = %v, want this directory linked to shop", link, err)
	}
	if got := fake.CommittedPaths(); !slices.Equal(got, []string{"index.js", "ocel.json"}) {
		t.Fatalf("committed %q, want the source without .env or .ocel", got)
	}
	for _, bearer := range fake.CloudBearers {
		if bearer != "Bearer "+clitest.FakeCloudToken {
			t.Fatalf("Ocel Cloud saw %q, want the token the console issued", bearer)
		}
	}

	again, err := runOnCloud(t, newInvocationLoggedInTo(fake), root, "ocel deploy")
	if !errors.As(err, &coded) || coded.Code != clierror.CodeCloudUnsupported {
		t.Fatalf("second run err = %v", err)
	}
	if len(fake.Created) != 1 {
		t.Fatalf("created %v, want the linked project reused", fake.Created)
	}
	if !strings.Contains(again, "already holds every file") {
		t.Fatalf("second run = %q, want nothing uploaded again", again)
	}
}

func newInvocationLoggedInTo(fake *clitest.FakeOcelCloud) commands.Invocation {
	invocation := clitest.NewInvocation()
	fake.LogIn(&invocation)
	return invocation
}

func TestAnOcelCloudRunSelectsTheConsoleProjectWithTheProjectsSlug(t *testing.T) {
	invocation, fake := loggedIn(t)
	fake.Projects = []*consolev1.Project{{Id: "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79", OrganizationId: "org_1", Name: "Shop", Slug: "shop"}}
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)

	if _, err := runOnCloud(t, invocation, root, "ocel build"); err == nil {
		t.Fatal("want the run stopped before a job")
	}
	if len(fake.Created) != 0 {
		t.Fatalf("created %v, want the existing project selected", fake.Created)
	}
	if link, _ := console.ReadLink(root, fake.ConsoleURL); link == nil || link.ProjectID != "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79" {
		t.Fatalf("link = %+v, want the existing project", link)
	}
}

func TestAnOcelCloudRunDeploysUnderTheActiveOrganizationOrAsksForOne(t *testing.T) {
	two := []console.Organization{{ID: "org_1", Name: "Acme Inc", Slug: "acme"}, {ID: "org_2", Name: "Globex", Slug: "globex"}}

	t.Run("the active organization", func(t *testing.T) {
		invocation, fake := loggedIn(t)
		fake.Organizations, fake.ActiveOrg = two, "org_2"
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)
		out, _ := runOnCloud(t, invocation, root, "ocel deploy")
		if !strings.Contains(out, "Globex") {
			t.Fatalf("output = %q, want Globex named", out)
		}
		if link, _ := console.ReadLink(root, fake.ConsoleURL); link == nil || link.OrganizationID != "org_2" {
			t.Fatalf("link = %+v, want org_2", link)
		}
		if !slices.Equal(fake.ActivatedOrgs, []string{"org_2"}) {
			t.Fatalf("activated %v, want org_2", fake.ActivatedOrgs)
		}
	})

	t.Run("none active", func(t *testing.T) {
		invocation, fake := loggedIn(t)
		fake.Organizations = two
		root := t.TempDir()
		clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)
		_, err := runOnCloud(t, invocation, root, "ocel deploy")
		var coded *clierror.Error
		if !errors.As(err, &coded) || coded.Code != clierror.CodeInputRequired || !strings.Contains(err.Error(), "ocel link --org") {
			t.Fatalf("err = %v, want ocel link --org asked for", err)
		}
		if len(fake.Created) != 0 || fake.Committed != nil {
			t.Fatal("linked or uploaded without an organization")
		}
	})
}
