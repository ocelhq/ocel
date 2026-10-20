package projects

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/console"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
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
	})

	t.Run("show reads the project's environments", func(t *testing.T) {
		project := onOcelCloud(t)
		invocation := clitest.NewInvocation()
		project.LogIn(&invocation)
		got := runProjects(t, invocation, project.Root, strings.NewReader(""), "show")
		if got.err != nil || !strings.Contains(got.stdout, "pr-1") {
			t.Fatalf("err = %v, stdout = %q, want the preview environments shown", got.err, got.stdout)
		}
	})

	t.Run("destroy takes both tiers behind the typed name and records it in the linked console project", func(t *testing.T) {
		project := onOcelCloud(t)
		if err := console.WriteLink(project.Root, console.Link{APIURL: project.Cloud.ConsoleURL, OrganizationID: "org_1", ProjectID: clitest.FixtureConsoleProjectID, ProjectName: clitest.FixtureSlug}); err != nil {
			t.Fatal(err)
		}
		invocation := askingInvocation()
		project.LogIn(&invocation)
		got := runDestroyCommand(t, invocation, project.Root, clitest.FixtureSlug+"\n")
		if got.err != nil || strings.Count(got.stdout, "Type the project name") != 1 || !strings.Contains(got.stdout, "Destroyed project "+clitest.FixtureSlug) {
			t.Fatalf("err = %v, stdout = %s, want both tiers destroyed behind one typed name", got.err, got.stdout)
		}
		if _, removed := removals(t, project.FakeProject); len(removed) != 2 {
			t.Fatalf("removed %d tiers, want both", len(removed))
		}
		sessions := map[string]bool{}
		for _, call := range project.Remote.Calls() {
			sessions[call.Session] = true
		}
		if len(sessions) != 1 {
			t.Fatalf("opened %d sessions on Ocel Cloud, want the one run open one", len(sessions))
		}
		events := project.Cloud.RecordedEvents()
		if len(events) != 2 || events[0].GetEvent().GetKind() != consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_DESTROYED {
			t.Fatalf("events = %v, want each tier's destroy recorded", events)
		}
	})
}
