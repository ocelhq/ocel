package commands_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
)

func insideCloudJob(t *testing.T, slug string) commands.Invocation {
	t.Helper()
	invocation := clitest.NewInvocation()
	invocation.Console.LoadCredentials = func() (console.Credentials, error) {
		t.Error("read a console login inside an Ocel Cloud job")
		return console.Credentials{}, console.ErrNotLoggedIn
	}
	invocation.CloudOffer = func(context.Context, console.Credentials) (*console.CloudOffer, error) {
		t.Error("asked a console for Ocel Cloud inside an Ocel Cloud job")
		return nil, nil
	}
	invocation.BuilderJob = &commands.BuilderJob{ID: "job_1", Slug: slug, ProvidersDir: t.TempDir()}
	return invocation
}

func TestInsideAnOcelCloudJobAProjectNamingNoProviderRunsThroughTheProviderOcel(t *testing.T) {
	invocation := insideCloudJob(t, "shop")
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)

	target, err := invocation.ResolveTarget(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if target.IsCloud() {
		t.Fatal("the target is Ocel Cloud again, which would upload the source from inside its own job")
	}
	if target.Project.Slug != "shop" || !target.Project.HasProvider() || target.Project.Provider.ID != commands.CloudProviderID {
		t.Fatalf("project = %+v, want shop through the provider %s", target.Project, commands.CloudProviderID)
	}

	loaded, err := invocation.LoadProject(t.Context(), root)
	if err != nil || !loaded.HasProvider() || loaded.Provider.ID != commands.CloudProviderID {
		t.Fatalf("loaded %+v, err = %v, want every load to run through the provider %s", loaded, err, commands.CloudProviderID)
	}
}

func TestInsideAnOcelCloudJobADirectoryWithNoConfigRunsAsTheJobsProject(t *testing.T) {
	invocation := insideCloudJob(t, "shop")
	root := filepath.Join(t.TempDir(), "source")
	clitest.WriteFile(t, filepath.Join(root, "package.json"), `{"name": "anything", "scripts": {"start": "node index.js"}}`)
	clitest.WriteFile(t, filepath.Join(root, "index.js"), "")

	target, err := invocation.ResolveTarget(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := target.Project
	if target.IsCloud() || cfg.Slug != "shop" || cfg.Dir != root || !cfg.HasProvider() || cfg.Provider.ID != commands.CloudProviderID {
		t.Fatalf("target = %+v, want %s run as shop through the provider %s", target, root, commands.CloudProviderID)
	}
	if len(cfg.Apps) != 1 || cfg.Apps[0].Name != "shop" || cfg.Apps[0].Path != "." {
		t.Fatalf("apps = %+v, want the root app named after the slug", cfg.Apps)
	}
}

func TestInsideAnOcelCloudJobARunRefusesAProjectThatIsNotTheJobs(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config string
		job    func(*commands.BuilderJob)
		want   string
	}{
		{name: "a config naming a provider", config: `{"slug": "shop", "provider": {"fake": {}}}`, want: `names the provider "fake"`},
		{name: "a config naming another slug", config: `{"slug": "cart"}`, want: "started for the project shop"},
		{name: "a job naming no slug", config: `{"slug": "shop"}`, job: func(j *commands.BuilderJob) { j.Slug = "" }, want: commands.CloudSlugEnvVar},
		{name: "a job with no providers", config: `{"slug": "shop"}`, job: func(j *commands.BuilderJob) { j.ProvidersDir = "" }, want: "OCEL_PROVIDERS_DIR"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			invocation := insideCloudJob(t, "shop")
			if tt.job != nil {
				tt.job(invocation.BuilderJob)
			}
			root := t.TempDir()
			clitest.WriteFile(t, filepath.Join(root, "ocel.json"), tt.config)

			_, err := invocation.ResolveTarget(t.Context(), root)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to name %q", err, tt.want)
			}
		})
	}
}

func TestReadBuilderJobReadsTheJobOnlyWhenOcelCloudSetsOne(t *testing.T) {
	env := map[string]string{}
	if job := commands.ReadBuilderJob(func(key string) string { return env[key] }); job != nil {
		t.Fatalf("job = %+v outside a job", job)
	}
	env = map[string]string{commands.CloudJobEnvVar: "job_1", commands.CloudSlugEnvVar: "shop", "OCEL_PROVIDERS_DIR": "/opt/ocel/providers"}
	job := commands.ReadBuilderJob(func(key string) string { return env[key] })
	if job == nil || *job != (commands.BuilderJob{ID: "job_1", Slug: "shop", ProvidersDir: "/opt/ocel/providers"}) {
		t.Fatalf("job = %+v, want job_1 for shop with its providers", job)
	}
}
