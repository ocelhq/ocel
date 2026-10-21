package deploy

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func previewOnOcelCloud(t *testing.T, branch string) (clitest.CloudProject, Dependencies, string) {
	t.Helper()
	project := clitest.SetUpCloudProject(t)
	clitest.Bootstrap(t, project.Provider, environment.TierPreview, fake.PartCache, fake.PartImages)
	identity := previewKey(t, branch)
	clitest.RecordEdgeStack(t, project.FakeProject, environment.TierPreview, fake.KindRelay)
	if err := stackrecords.Write(context.Background(), project.Provider.KeyValues(), environment.TierPreview, clitest.FixtureSlug, naming.InfraStack(identity), stackrecords.Stack{}); err != nil {
		t.Fatal(err)
	}
	if err := stackrecords.EnsureLifecycle(context.Background(), project.Provider.KeyValues(), environment.TierPreview, clitest.FixtureSlug, identity, stackrecords.LifecycleEphemeral, ""); err != nil {
		t.Fatal(err)
	}
	dependencies := previewDependencies(branch, "")
	project.LogIn(&dependencies.Invocation)
	return project, dependencies, identity
}

func TestPreviewLsRmAndPruneRunOnOcelCloudThroughTheProviderItServes(t *testing.T) {
	t.Run("ls lists the previews Ocel Cloud runs", func(t *testing.T) {
		project, dependencies, identity := previewOnOcelCloud(t, "feature/login")
		var stdout bytes.Buffer
		if err := ListPreviews(t.Context(), dependencies, project.Root, &stdout); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(stdout.String(), identity) {
			t.Fatalf("stdout = %q, want %s listed", stdout.String(), identity)
		}
		requireTokenScopes(t, project.Cloud, "cloud.apps.read")
	})

	t.Run("rm tears down the current branch's preview and records it in the linked console project", func(t *testing.T) {
		project, dependencies, identity := previewOnOcelCloud(t, "feature/login")
		if err := console.WriteLink(project.Root, console.Link{APIURL: project.Cloud.ConsoleURL, OrganizationID: "org_1", ProjectID: clitest.FixtureConsoleProjectID, ProjectName: clitest.FixtureSlug}); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runPreviewRemove(t.Context(), dependencies, project.Root, previewRemoveOptions{}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		left, err := stackrecords.PreviewEnvironments(context.Background(), project.Provider.KeyValues(), clitest.FixtureSlug)
		if err != nil || len(left) != 0 {
			t.Fatalf("previews left = %+v, err = %v, want %s torn down", left, err, identity)
		}
		events := project.Cloud.RecordedEvents()
		if len(events) != 1 || events[0].GetEvent().GetKind() != consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_PREVIEW_REMOVED || events[0].GetEvent().GetEnvironment().GetIdentity() != identity {
			t.Fatalf("events = %v, want the preview's removal recorded", events)
		}
		requireTokenScopes(t, project.Cloud, "cloud.apps.read", "cloud.apps.write")
	})

	t.Run("prune reaches Ocel Cloud", func(t *testing.T) {
		project, dependencies, _ := previewOnOcelCloud(t, "feature/login")
		var stdout bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
		if err := runPreviewPrune(t.Context(), dependencies, project.Root, previewPruneOptions{keep: 1, yes: true}, &stdout, strings.NewReader("")); err != nil {
			t.Fatalf("err = %v; stdout=%s", err, stdout.String())
		}
		if !strings.Contains(stdout.String(), "Pruned the promotions of preview") {
			t.Fatalf("stdout = %q, want the prune reported", stdout.String())
		}
		requireTokenScopes(t, project.Cloud, "cloud.apps.read", "cloud.apps.write")
	})
}

func requireTokenScopes(t *testing.T, cloud *clitest.FakeOcelCloud, want ...string) {
	t.Helper()
	if len(cloud.TokenScopes) == 0 || slices.ContainsFunc(cloud.TokenScopes, func(got []string) bool { return !slices.Equal(got, want) }) {
		t.Fatalf("tokens issued with scopes %v, want each with exactly %v", cloud.TokenScopes, want)
	}
}
