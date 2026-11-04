package projects

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/console"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

func onOcelCloud(t *testing.T) clitest.CloudProject {
	t.Helper()
	project := clitest.SetUpCloudProject(t)
	recordBothTiers(t, project.FakeProject)
	return project
}

func TestProjectsRunOnOcelCloudThroughTheProviderItServes(t *testing.T) {
	t.Run("ls lists the projects Ocel Cloud runs", func(t *testing.T) {
		project := onOcelCloud(t)
		invocation := clitest.NewInvocation()
		project.LogIn(&invocation)
		got := runProjects(t, invocation, project.Root, strings.NewReader(""), "ls")
		if got.err != nil || !strings.Contains(got.stdout, clitest.FixtureSlug) {
			t.Fatalf("err = %v, stdout = %q, want %s listed", got.err, got.stdout, clitest.FixtureSlug)
		}
		requireTokenScopes(t, project.Cloud, "apps.read")
	})

	t.Run("a run names the organization, the console and the person, as a deploy does", func(t *testing.T) {
		project := onOcelCloud(t)
		project.Cloud.Organizations = append(project.Cloud.Organizations, console.Organization{ID: "org_2", Name: "Globex", Slug: "globex"})
		project.Cloud.ActiveOrg = "org_2"
		project.Cloud.Link(t, project.Root)
		invocation := clitest.NewInvocation()
		project.LogIn(&invocation)
		got := runProjects(t, invocation, project.Root, strings.NewReader(""), "ls")
		if got.err != nil {
			t.Fatal(got.err)
		}
		host := strings.TrimPrefix(project.Cloud.URL, "http://")
		for _, want := range []string{"Ocel Cloud", "Acme Inc", host, "as dev@acme.test"} {
			if !strings.Contains(got.run, want) || !strings.Contains(got.stdout, "Ocel Cloud Acme Inc") {
				t.Fatalf("run = %q, stdout = %q, want %q named as a deploy names it", got.run, got.stdout, want)
			}
		}
		if activated := project.Cloud.ActivatedOrgs; len(activated) != 0 {
			t.Fatalf("activated %v, want the console's active organization left alone", activated)
		}
		if orgs := project.Cloud.TokenOrgs; len(orgs) == 0 || slices.ContainsFunc(orgs, func(org string) bool { return org != "org_1" }) {
			t.Fatalf("tokens issued for %v, want each for org_1, the linked project's organization", orgs)
		}
	})

	t.Run("show reads the project's environments", func(t *testing.T) {
		project := onOcelCloud(t)
		invocation := clitest.NewInvocation()
		project.LogIn(&invocation)
		got := runProjects(t, invocation, project.Root, strings.NewReader(""), "show")
		if got.err != nil || !strings.Contains(got.stdout, "pr-1") {
			t.Fatalf("err = %v, stdout = %q, want the preview environments shown", got.err, got.stdout)
		}
		requireTokenScopes(t, project.Cloud, "apps.read")
	})

	t.Run("destroy dispatches a job for each tier behind one typed name", func(t *testing.T) {
		project := onOcelCloud(t)
		project.Cloud.Link(t, project.Root)
		clitest.WriteFile(t, filepath.Join(project.Root, "ocel.json"), `{"slug": "`+clitest.FixtureSlug+`"}`)
		invocation := askingInvocation()
		project.LogIn(&invocation)
		got := runDestroyCommand(t, invocation, project.Root, clitest.FixtureSlug+"\n")
		if got.err != nil || strings.Count(got.stdout, "Type the project name") != 1 || !strings.Contains(got.stdout, "Destroyed project "+clitest.FixtureSlug) {
			t.Fatalf("err = %v, stdout = %s, want both tiers destroyed behind one typed name", got.err, got.stdout)
		}
		dispatched := project.Cloud.Dispatched
		if len(dispatched) != 2 || dispatched[0].GetTier() != environmentv1.Tier_TIER_PRODUCTION || dispatched[1].GetTier() != environmentv1.Tier_TIER_PREVIEW ||
			dispatched[0].GetJob().GetCommand() != runnerv1.JobCommand_JOB_COMMAND_PROJECTS_DESTROY || dispatched[0].GetRequestId() == dispatched[1].GetRequestId() {
			t.Fatalf("dispatched %v, want one destroy job per tier, each its own request", clitest.DescribeDispatched(dispatched))
		}
		if _, removed := removals(t, project.FakeProject); len(removed) != 0 {
			t.Fatalf("removed %d tiers here, want the jobs to remove them", len(removed))
		}
	})

	t.Run("destroy dispatches nothing when the typed name does not match", func(t *testing.T) {
		project := onOcelCloud(t)
		project.Cloud.Link(t, project.Root)
		clitest.WriteFile(t, filepath.Join(project.Root, "ocel.json"), `{"slug": "`+clitest.FixtureSlug+`"}`)
		invocation := askingInvocation()
		project.LogIn(&invocation)
		got := runDestroyCommand(t, invocation, project.Root, "other\n")
		if got.err != nil || len(project.Cloud.Dispatched) != 0 || project.Cloud.Runner.Committed != nil {
			t.Fatalf("err = %v, dispatched %v, want nothing uploaded or dispatched", got.err, clitest.DescribeDispatched(project.Cloud.Dispatched))
		}
	})
}

func requireTokenScopes(t *testing.T, cloud *clitest.FakeConsole, want ...string) {
	t.Helper()
	if len(cloud.TokenScopes) == 0 || slices.ContainsFunc(cloud.TokenScopes, func(got []string) bool { return !slices.Equal(got, want) }) {
		t.Fatalf("tokens issued with scopes %v, want each with exactly %v", cloud.TokenScopes, want)
	}
}
