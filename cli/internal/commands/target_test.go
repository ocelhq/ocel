package commands_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/project"
	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func loggedIn(t *testing.T) (commands.Invocation, *clitest.FakeConsole) {
	t.Helper()
	fake := clitest.ServeConsole(t)
	invocation := clitest.NewInvocation()
	fake.LogIn(&invocation)
	return invocation, fake
}

func noOffer(invocation *commands.Invocation) {
	invocation.CloudOffer = func(context.Context, console.Credentials) (*console.CloudOffer, error) { return nil, nil }
}

func loggedOut(invocation *commands.Invocation) {
	invocation.Console.LoadCredentials = func() (console.Credentials, error) { return console.Credentials{}, console.ErrNotLoggedIn }
}

func TestTheTargetIsTheConfiguredProviderOrOcelCloudWhenNoneIsNamed(t *testing.T) {
	for _, tt := range []struct {
		name      string
		config    string
		arrange   func(*commands.Invocation)
		wantCloud bool
	}{
		{name: "a named provider, logged in to a console offering Ocel Cloud", config: `{"slug": "shop", "provider": {"fake": {}}}`},
		{name: "no provider, logged in to a console offering Ocel Cloud", config: `{"slug": "shop"}`, wantCloud: true},
		{name: "no provider, logged in to a console offering none", config: `{"slug": "shop"}`, arrange: noOffer},
		{name: "no provider, not logged in", config: `{"slug": "shop"}`, arrange: loggedOut},
	} {
		t.Run(tt.name, func(t *testing.T) {
			invocation, _ := loggedIn(t)
			if tt.arrange != nil {
				tt.arrange(&invocation)
			}
			root := t.TempDir()
			clitest.WriteFile(t, filepath.Join(root, "ocel.json"), tt.config)

			target, err := invocation.ResolveTarget(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			if target.IsCloud() != tt.wantCloud {
				t.Fatalf("cloud = %v, want %v", target.IsCloud(), tt.wantCloud)
			}
			if target.Project == nil || target.Project.Slug != "shop" || target.Derived != "" {
				t.Fatalf("project = %+v, derived %q, want the loaded config", target.Project, target.Derived)
			}
		})
	}
}

func TestWithNoConfigALoginToOcelCloudRunsTheDirectoryAsAProjectNamedAfterIt(t *testing.T) {
	invocation, fake := loggedIn(t)
	root := filepath.Join(t.TempDir(), "My Shop")
	clitest.WriteFile(t, filepath.Join(root, "index.js"), "")

	target, err := invocation.ResolveTarget(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !target.IsCloud() || target.Project.Slug != "my-shop" || target.Project.Dir != root {
		t.Fatalf("target = %+v, want Ocel Cloud running %s as my-shop", target, root)
	}
	if target.Cloud.ConsoleURL != fake.URL || target.Cloud.Offer.APIURL != fake.Runner.URL {
		t.Fatalf("cloud = %+v, want the console logged in to and its offer", target.Cloud)
	}
	if !strings.Contains(target.Derived, "my-shop") || !strings.Contains(target.Derived, "named after this directory") {
		t.Fatalf("derived = %q, want it to say where the slug came from", target.Derived)
	}
	if _, err := os.Stat(filepath.Join(root, "ocel.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a config was written: %v", err)
	}

	loggedOut(&invocation)
	var coded *clierror.Error
	if target, err := invocation.ResolveTarget(t.Context(), root); !errors.As(err, &coded) || coded.Code != clierror.CodeProjectNoConfig || target.Project != nil {
		t.Fatalf("logged out: target = %+v, err = %v, want the missing config reported", target, err)
	}
}

func TestATargetThatCannotBeReadIsRefusedAsTheRunReadsIt(t *testing.T) {
	invocation, _ := loggedIn(t)
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop", "nope": true}`)

	target, err := invocation.ResolveTarget(t.Context(), root)
	var coded *clierror.Error
	if !errors.As(err, &coded) || coded.Code != clierror.CodeProjectInvalidConfig || target.Project != nil {
		t.Fatalf("target = %+v, err = %v, want the invalid config refused", target, err)
	}

	invocation.ConfigPath = func() string { return filepath.Join(root, "missing.json") }
	if _, err := invocation.ResolveTarget(t.Context(), root); err == nil || !strings.Contains(err.Error(), "missing.json") {
		t.Fatalf("err = %v, want the --config that names nothing refused", err)
	}
}

func TestATypeScriptConfigWithoutNodeRunsOnOcelCloudOnlyAsTheConsoleProjectItIsLinkedTo(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, tt := range []struct {
		name      string
		config    string
		link      string
		wantCloud bool
		wantSlug  string
	}{
		{name: "a config that mentions a provider is never guessed at", config: `export default { slug: "shop", provider: { aws: {} } };`},
		{name: "a linked project takes the console project's name", config: `export default { slug: "shop" };`, link: "Shop Front", wantCloud: true, wantSlug: "shop-front"},
		{name: "an unlinked project is not named after its directory", config: `export default { slug: "shop" };`},
		{name: "a project linked to a name that makes no slug is not guessed at", config: `export default { slug: "shop" };`, link: "!!!"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			invocation, fake := loggedIn(t)
			root := filepath.Join(t.TempDir(), "web")
			clitest.WriteFile(t, filepath.Join(root, "ocel.config.ts"), tt.config)
			if tt.link != "" {
				if err := console.WriteLink(root, console.Link{APIURL: fake.URL, OrganizationID: "org_1", ProjectID: "p", ProjectName: tt.link}); err != nil {
					t.Fatal(err)
				}
			}
			target, err := invocation.ResolveTarget(t.Context(), root)
			if !tt.wantCloud {
				if err == nil || !strings.Contains(err.Error(), "install node, or write ocel.json or ocel.yaml") || target.IsCloud() {
					t.Fatalf("target = %+v, err = %v, want a refusal that names node and the configs that need none", target, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if target.Project.Slug != tt.wantSlug || !strings.Contains(target.Derived, "ocel.config.ts needs node") {
				t.Fatalf("slug = %q, derived = %q, want %s and why", target.Project.Slug, target.Derived, tt.wantSlug)
			}
		})
	}
}

func TestACommandOcelCloudCannotRunYetRefusesItAndOneWithNoTargetOffersLogin(t *testing.T) {
	invocation, _ := loggedIn(t)
	root := t.TempDir()
	clitest.WriteFile(t, filepath.Join(root, "ocel.json"), `{"slug": "shop"}`)
	cfg, err := invocation.LoadProject(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}

	_, err = invocation.RequireProvider(t.Context(), cfg, "ocel bindings")
	var coded *clierror.Error
	if !errors.As(err, &coded) || coded.Code != clierror.CodeCloudUnsupported || !strings.Contains(err.Error(), "`ocel bindings` does not run on Ocel Cloud yet") {
		t.Fatalf("err = %v, want ocel bindings refused on Ocel Cloud", err)
	}
	err = invocation.WithProvider(t.Context(), cfg, "ocel bindings", commands.OpenOptions{}, func(context.Context, commands.ProviderRun) error {
		t.Fatal("opened a provider for a project that deploys to Ocel Cloud")
		return nil
	})
	if !errors.As(err, &coded) || coded.Code != clierror.CodeCloudUnsupported {
		t.Fatalf("WithProvider err = %v, want the refusal", err)
	}

	loggedOut(&invocation)
	_, err = invocation.RequireProvider(t.Context(), cfg, "ocel bindings")
	if err == nil || !strings.Contains(err.Error(), "no provider configured") || !strings.Contains(err.Error(), "`ocel login`") {
		t.Fatalf("err = %v, want the missing provider with ocel login offered", err)
	}

	named := *cfg
	named.Provider = &project.Provider{ID: "fake"}
	if provider, err := invocation.RequireProvider(t.Context(), &named, "ocel bindings"); err != nil || provider.ID != "fake" {
		t.Fatalf("provider = %v, err = %v, want the configured one", provider, err)
	}
}

func TestACommandThatRunsOnOcelCloudOpensASessionOnTheProviderOcelCloudServes(t *testing.T) {
	project := clitest.SetUpCloudProject(t)
	invocation := clitest.NewInvocation()
	project.LogIn(&invocation)

	target, err := invocation.ResolveTarget(t.Context(), project.Root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := target.Project
	if cfg.Slug != clitest.FixtureSlug || cfg.HasProvider() {
		t.Fatalf("project = %+v, want the unconfigured directory named after itself", cfg)
	}
	err = invocation.WithProvider(t.Context(), cfg, "ocel preview ls", commands.OpenOptions{Cloud: target.Cloud}, func(ctx context.Context, p commands.ProviderRun) error {
		if p.Provider.Name() != commands.CloudProviderID {
			t.Errorf("provider = %q, want %q", p.Provider.Name(), commands.CloudProviderID)
		}
		return p.Provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
			_, err := client.ListEnvironments(ctx, &contractv1.ListEnvironmentsRequest{Slug: cfg.Slug})
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := project.Remote.Calls()
	if len(calls) != 2 || !strings.HasSuffix(calls[0].Procedure, "/Configure") || calls[0].Authorization != "Bearer "+clitest.FakeCloudToken+"|apps.read" {
		t.Fatalf("calls = %+v, want Configure and the call, with the token the console issued", calls)
	}
}

func TestASessionOnAnOcelCloudThatPublishesAnotherOcelIsRefusedBeforeItOpens(t *testing.T) {
	project := clitest.SetUpCloudProject(t)
	project.Cloud.RunnerVersion = "9.9.9"
	invocation := clitest.NewInvocation()
	project.LogIn(&invocation)
	target, err := invocation.ResolveTarget(t.Context(), project.Root)
	if err != nil {
		t.Fatal(err)
	}

	err = invocation.WithProvider(t.Context(), target.Project, "ocel preview ls", commands.OpenOptions{Cloud: target.Cloud}, func(context.Context, commands.ProviderRun) error {
		t.Error("ran a command on a session Ocel Cloud does not serve")
		return nil
	})
	if got := clierror.NewRunError(err); got.GetCode() != clierror.CodeCloudVersionUnserved || !strings.Contains(got.GetMessage(), "serves ocel 9.9.9") {
		t.Fatalf("run error = %v, want %s naming the version Ocel Cloud serves", got, clierror.CodeCloudVersionUnserved)
	}
	if calls := project.Remote.Calls(); len(calls) != 0 {
		t.Fatalf("called %v, want no session opened", calls)
	}
}

func TestASessionOnOcelCloudHoldsTheScopesItsCommandDeclaresAndIsReusedOnlyWhereTheyReach(t *testing.T) {
	project := clitest.SetUpCloudProject(t)
	invocation := clitest.NewInvocation()
	project.LogIn(&invocation)
	target, err := invocation.ResolveTarget(t.Context(), project.Root)
	if err != nil {
		t.Fatal(err)
	}

	reading := commands.OpenOptions{Cloud: target.Cloud}
	writing := commands.OpenOptions{Cloud: target.Cloud, CloudScopes: []scopev1.Scope{scopev1.Scope_SCOPE_APPS_WRITE}}
	err = invocation.WithProvider(t.Context(), target.Project, "ocel test", reading, func(ctx context.Context, p commands.ProviderRun) error {
		for _, opts := range []commands.OpenOptions{writing, reading, writing} {
			provider, _, err := invocation.OpenProvider(ctx, p.Check, target.Project, opts)
			if err != nil {
				return err
			}
			if err := provider.Call(ctx, func(client contractv1connect.ProviderServiceClient) error {
				_, err := client.ListEnvironments(ctx, &contractv1.ListEnvironmentsRequest{Slug: target.Project.Slug})
				return err
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"apps.read"}, {"apps.read", "apps.write"}}
	if !slices.EqualFunc(project.Cloud.TokenScopes, want, slices.Equal) {
		t.Fatalf("tokens issued with scopes %v, want %v: a reading session, then one writing session the later opens reuse", project.Cloud.TokenScopes, want)
	}
	sessions := map[string]bool{}
	for _, call := range project.Remote.Calls() {
		sessions[call.Session] = true
	}
	if len(sessions) != 2 {
		t.Fatalf("opened %d sessions, want 2", len(sessions))
	}
}

func TestACommandThatDoesNotRunOnOcelCloudRefusesItWithoutOpeningASession(t *testing.T) {
	project := clitest.SetUpCloudProject(t)
	invocation := clitest.NewInvocation()
	project.LogIn(&invocation)
	target, err := invocation.ResolveTarget(t.Context(), project.Root)
	if err != nil {
		t.Fatal(err)
	}

	err = invocation.WithProvider(t.Context(), target.Project, "ocel bindings ls", commands.OpenOptions{}, func(context.Context, commands.ProviderRun) error {
		t.Fatal("opened a provider")
		return nil
	})
	var coded *clierror.Error
	if !errors.As(err, &coded) || coded.Code != clierror.CodeCloudUnsupported {
		t.Fatalf("err = %v, want cloud.unsupported", err)
	}
	if calls := project.Remote.Calls(); len(calls) != 0 {
		t.Fatalf("calls = %+v, want none", calls)
	}
}

func TestAConsoleOfferingOcelCloudWithNoProviderURLRefusesACommandThatRunsOnIt(t *testing.T) {
	project := clitest.SetUpCloudProject(t)
	project.Cloud.ProviderURL = ""
	invocation := clitest.NewInvocation()
	project.LogIn(&invocation)
	target, err := invocation.ResolveTarget(t.Context(), project.Root)
	if err != nil {
		t.Fatal(err)
	}

	err = invocation.WithProvider(t.Context(), target.Project, "ocel preview ls", commands.OpenOptions{Cloud: target.Cloud}, func(context.Context, commands.ProviderRun) error {
		t.Fatal("opened a provider")
		return nil
	})
	var coded *clierror.Error
	if !errors.As(err, &coded) || coded.Code != clierror.CodeCloudUnsupported || !strings.Contains(err.Error(), "no provider URL") {
		t.Fatalf("err = %v, want cloud.unsupported naming the missing provider URL", err)
	}
}
