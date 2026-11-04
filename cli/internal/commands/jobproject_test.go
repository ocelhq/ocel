package commands_test

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/jobinput"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/jobcontext"
)

func insideJob(t *testing.T, slug string) commands.Invocation {
	t.Helper()
	invocation := clitest.NewInvocation()
	invocation.Console.LoadCredentials = func() (console.Credentials, error) {
		t.Error("read a console login inside a job")
		return console.Credentials{}, console.ErrNotLoggedIn
	}
	invocation.CloudOffer = func(context.Context, console.Credentials) (*console.CloudOffer, error) {
		t.Error("asked a console for Ocel Cloud inside a job")
		return nil, nil
	}
	invocation.JobContext = &jobcontext.Context{Job: &jobcontext.Job{ID: "job_1", Slug: slug}}
	return invocation
}

func TestInsideAJobAProjectNamingNoProviderRunsThroughTheProviderOcel(t *testing.T) {
	invocation := insideJob(t, "shop")
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
}

func TestInsideAJobAProjectNamingAProviderRunsThroughIt(t *testing.T) {
	invocation := insideJob(t, "shop")
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop", "provider": {"fake": {}}}`)

	loaded, err := invocation.LoadProject(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Provider.ID != "fake" {
		t.Fatalf("provider = %+v, want the one the project names", loaded.Provider)
	}
}

func TestInsideAJobADirectoryWithNoConfigRunsAsTheJobsProject(t *testing.T) {
	invocation := insideJob(t, "shop")
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
}

func TestInsideAJobARunRefusesAProjectOfAnotherSlugAsAnInvalidConfig(t *testing.T) {
	invocation := insideJob(t, "shop")
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "cart"}`)

	_, err := invocation.ResolveTarget(t.Context(), root)
	if err == nil || !strings.Contains(err.Error(), "started for the project shop") {
		t.Fatalf("err = %v, want the job's project named", err)
	}
	if code := clierror.NewRunError(err).GetCode(); code != clierror.CodeProjectInvalidConfig {
		t.Errorf("code = %q, want %q", code, clierror.CodeProjectInvalidConfig)
	}
}

func writeJobToken(t *testing.T, subject string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"` + subject + `","aud":"ocel-job"}`))
	if err := os.WriteFile(path, []byte("e30."+payload+".c2ln"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInsideAJobEveryPolicyTheInvocationMakesPutsItsQuestionsToTheConsole(t *testing.T) {
	invocation := insideJob(t, "shop")
	invocation.JobContext.Job.Input = jobinput.New(strings.NewReader(""))
	for _, policy := range []consent.Policy{
		invocation.PlanPolicy("ocel rollback", false, io.Discard, strings.NewReader("")),
		invocation.Policy("ocel preview rm", false, io.Discard, strings.NewReader("")),
	} {
		if err := policy.Refuse(); err != nil {
			t.Errorf("%s: Refuse() = %v, want the console to answer", policy.Command, err)
		}
		if err := policy.RefuseQuestion("Remove it?", "before it removes it"); err != nil {
			t.Errorf("%s: RefuseQuestion() = %v, want the console to answer", policy.Command, err)
		}
	}
}
