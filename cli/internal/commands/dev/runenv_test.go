//go:build unix

package dev

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/commands/deploy"
	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/environment"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

type forwards struct {
	mutex sync.Mutex
	open  int
	asked []string
}

func (f *forwards) openNow() int {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return f.open
}

func forwardingPorts(t *testing.T, fixture clitest.FakeProject) *forwards {
	t.Helper()
	seen := &forwards{}
	fixture.Provider.WithHooks(func(h *provider.Hooks) {
		h.ForwardPorts = func(_ context.Context, req provider.PortForwardRequest) ([]provider.PortForward, error) {
			seen.mutex.Lock()
			defer seen.mutex.Unlock()
			var out []provider.PortForward
			for _, binding := range req.Bindings {
				seen.open++
				seen.asked = append(seen.asked, binding.Name)
				out = append(out, provider.PortForward{Binding: binding.Name, LocalAddress: "127.0.0.1:41234", Close: func() {
					seen.mutex.Lock()
					defer seen.mutex.Unlock()
					seen.open--
				}})
			}
			return out, nil
		}
	})
	return seen
}

func setUpDeployedProject(t *testing.T) clitest.FakeProject {
	t.Helper()
	fixture := clitest.SetUpProject(t)
	clitest.Bootstrap(t, fixture.Provider, environment.TierPreview, fake.FeatureCache, fake.FeatureImages)
	clitest.WriteUsageMonorepo(t, fixture.Root)
	clitest.WriteFile(t, filepath.Join(fixture.Root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { fake: {} },
  domains: { production: "app.acme.com", preview: "*.preview.acme.com" },
};
`)
	return fixture
}

func dependenciesOf(invocation commands.Invocation) Dependencies {
	return Dependencies{
		Invocation:          invocation,
		CollectDeclarations: declaration.Collect,
		ReadGitBranch:       func(string) (string, error) { return "feature/login", nil },
		DiscoverPRNumber:    func() string { return "" },
	}
}

func deployProject(t *testing.T, fixture clitest.FakeProject, invocation commands.Invocation) {
	t.Helper()
	command := deploy.NewCommand(deploy.Dependencies{
		Invocation: invocation,
		BuildApps: func(context.Context, *project.Project, map[string]build.AppVariables, map[string]string, build.HostedWorkers, build.Host, build.Log) (build.Output, error) {
			return build.Output{}, nil
		},
		RefuseUnbuildableImages: build.RefuseUnbuildableImages,
		ReadPrebuilt:            build.ReadPrebuilt,
		BuildID:                 build.BuildID,
		CollectDeclarations:     declaration.Collect,
		DiscoverPRNumber:        func() string { return "" },
	})
	var stdout bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stdout)
	command.SetOut(&stdout)
	command.SetErr(&stdout)
	command.SetArgs([]string{"--yes"})
	t.Chdir(fixture.Root)
	if err := command.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("ocel deploy err = %v; out=%s", err, stdout.String())
	}
}

func runInEnvironment(t *testing.T, fixture clitest.FakeProject, invocation commands.Invocation, args ...string) (string, error) {
	t.Helper()
	command := NewRunCommand(dependenciesOf(invocation))
	var stdout bytes.Buffer
	clitest.AttachTerminalSink(invocation, &stdout)
	command.SetOut(&stdout)
	command.SetErr(&stdout)
	command.SetIn(strings.NewReader(""))
	command.SetArgs(args)
	t.Chdir(fixture.Root)
	err := command.ExecuteContext(context.Background())
	return stdout.String(), err
}

func TestRunInADeployedEnvironmentGivesTheCommandItsBindingsThroughTheLiveDirAndClosesTheForwardsAfterwards(t *testing.T) {
	fixture := setUpDeployedProject(t)
	seen := forwardingPorts(t, fixture)
	invocation := clitest.NewInvocation()
	deployProject(t, fixture, invocation)
	out := filepath.Join(t.TempDir(), "seen")

	got, err := runInEnvironment(t, fixture, clitest.NewInvocation(), "--env", "production", "--", "sh", "-c",
		`cat "$OCEL_LIVE_DIR/OCEL_RESOURCE_POSTGRES_main" > `+out+`; echo >> `+out+`; echo "[${OCEL_RESOURCE_POSTGRES_main-unset}]" >> `+out)

	if err != nil {
		t.Fatalf("ocel run err = %v; out=%s", err, got)
	}
	raw := strings.SplitN(readFile(t, out), "\n", 2)
	var binding bindingsv1.Binding
	if err := protojson.Unmarshal([]byte(raw[0]), &binding); err != nil || binding.GetPostgres().GetPort() != 41234 || binding.GetName() != "main" {
		t.Errorf("the command read %q as main's binding (%v), want the one forwarded to port 41234 under the name the app declares", raw[0], err)
	}
	if !strings.Contains(raw[1], "[unset]") {
		t.Errorf("the command's environment held the binding (%q), want it only in the live dir", raw[1])
	}
	deadline := time.Now().Add(2 * time.Second)
	for seen.openNow() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if open := seen.openNow(); open != 0 {
		t.Errorf("%d forwards are open after the command exited, want them closed", open)
	}
}

func TestRunInADeployedEnvironmentHidesWhatTheBindingsKeepSecretInTheCommandsOutput(t *testing.T) {
	fixture := setUpDeployedProject(t)
	forwardingPorts(t, fixture)
	deployProject(t, fixture, clitest.NewInvocation())
	out := filepath.Join(t.TempDir(), "seen")

	got, err := runInEnvironment(t, fixture, clitest.NewInvocation(), "--env", "production", "--", "sh", "-c",
		`cp "$OCEL_LIVE_DIR/OCEL_RESOURCE_POSTGRES_main" `+out+`; cat `+out+`; cat `+out+` >&2; printf %s "$(cat `+out+`)"`)

	if err != nil {
		t.Fatalf("ocel run err = %v; out=%s", err, got)
	}
	var binding bindingsv1.Binding
	if err := protojson.Unmarshal([]byte(readFile(t, out)), &binding); err != nil || binding.GetPostgres().GetPassword() == "" {
		t.Fatalf("the command read %q as main's binding (%v), want one with a password", readFile(t, out), err)
	}
	if password := binding.GetPostgres().GetPassword(); strings.Contains(got, password) {
		t.Errorf("ocel run printed main's password in %q, want it hidden on stdout and stderr", got)
	}
}

func TestRunInADeployedEnvironmentExitsWithTheCommandsExitCode(t *testing.T) {
	fixture := setUpDeployedProject(t)
	forwardingPorts(t, fixture)
	deployProject(t, fixture, clitest.NewInvocation())

	_, err := runInEnvironment(t, fixture, clitest.NewInvocation(), "--env", "production", "--", "sh", "-c", "exit 7")

	if err == nil || !strings.Contains(err.Error(), "7") {
		t.Fatalf("ocel run err = %v, want the command's exit code 7", err)
	}
}

func TestRunInAnEnvironmentNothingIsDeployedToSaysSoAndRunsNothing(t *testing.T) {
	fixture := setUpDeployedProject(t)
	forwardingPorts(t, fixture)
	ran := filepath.Join(t.TempDir(), "ran")

	_, err := runInEnvironment(t, fixture, clitest.NewInvocation(), "--env", "production", "--", "touch", ran)

	if err == nil || !strings.Contains(err.Error(), "not deployed yet") {
		t.Fatalf("ocel run err = %v, want it to say the environment is not deployed", err)
	}
	if _, statErr := os.Stat(ran); statErr == nil {
		t.Error("the command ran against an environment with nothing deployed")
	}
}

func TestRunInAnEnvironmentOfAProviderThatForwardsNoPortSaysSoAndRunsNothing(t *testing.T) {
	fixture := setUpDeployedProject(t)
	ran := filepath.Join(t.TempDir(), "ran")

	_, err := runInEnvironment(t, fixture, clitest.NewInvocation(), "--env", "production", "--", "touch", ran)

	if err == nil || !strings.Contains(err.Error(), "forwards no port") {
		t.Fatalf("ocel run err = %v, want it to say the provider forwards no port", err)
	}
	if _, statErr := os.Stat(ran); statErr == nil {
		t.Error("the command ran without a way to reach the environment")
	}
}

func TestRunInADeployedEnvironmentAsksForTheForwardsOfThatEnvironmentsTier(t *testing.T) {
	fixture := setUpDeployedProject(t)
	forwardingPorts(t, fixture)
	deployProject(t, fixture, clitest.NewInvocation())
	asked := make(chan provider.PortForwardRequest, 1)
	fixture.Provider.WithHooks(func(h *provider.Hooks) {
		forward := h.ForwardPorts
		h.ForwardPorts = func(ctx context.Context, req provider.PortForwardRequest) ([]provider.PortForward, error) {
			asked <- req
			return forward(ctx, req)
		}
	})

	got, err := runInEnvironment(t, fixture, clitest.NewInvocation(), "--env", "production", "--", "true")

	if err != nil {
		t.Fatalf("ocel run err = %v; out=%s", err, got)
	}
	req := <-asked
	if req.Tier != environment.TierProduction || req.ReportFailure == nil {
		t.Errorf("the provider was asked to forward in tier %q with ReportFailure set = %v, want production and a way to report a failed forward", req.Tier, req.ReportFailure != nil)
	}
}

func TestRunInADeployedEnvironmentWhoseForwardFailsWhileTheCommandRunsFailsAndSaysWhy(t *testing.T) {
	fixture := setUpDeployedProject(t)
	forwardingPorts(t, fixture)
	deployProject(t, fixture, clitest.NewInvocation())
	fixture.Provider.WithHooks(func(h *provider.Hooks) {
		forward := h.ForwardPorts
		h.ForwardPorts = func(ctx context.Context, req provider.PortForwardRequest) ([]provider.PortForward, error) {
			forwards, err := forward(ctx, req)
			req.ReportFailure(errors.New("the bastion task stopped: Essential container in task exited"))
			return forwards, err
		}
	})

	got, err := runInEnvironment(t, fixture, clitest.NewInvocation(), "--env", "production", "--", "sleep", "1")

	if err == nil || !strings.Contains(err.Error(), "the bastion task stopped") {
		t.Fatalf("ocel run err = %v, want the failed forward's reason; out=%s", err, got)
	}
}

func TestRunInAnEnvironmentThatIsNeitherProductionNorPreviewIsRefused(t *testing.T) {
	fixture := setUpDeployedProject(t)

	_, err := runInEnvironment(t, fixture, clitest.NewInvocation(), "--env", "staging", "--", "true")

	if err == nil || !strings.Contains(err.Error(), "production or preview") {
		t.Fatalf("ocel run err = %v, want the two environments named", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	read, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(read)
}
