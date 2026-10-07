package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const buildID = "0123456789abcdef0123456789abcdef"

func aStack(t *testing.T, app provider.AppSpec) provider.StackSpec {
	t.Helper()
	stack, err := naming.ParseStackName("prod--web--r0a1b2c3d")
	if err != nil {
		t.Fatal(err)
	}
	return provider.StackSpec{
		Ref:  provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: stack},
		Kind: provider.StackApp,
		App:  &app,
	}
}

func anApp() provider.AppSpec {
	return provider.AppSpec{
		App:             "web",
		Compute:         provider.ComputeContainer,
		BuildID:         buildID,
		Image:           loadedImageRef,
		HealthCheckPath: "/healthz",
	}
}

func over(machine *box) *vps.Provider {
	return vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
}

func TestStartingAnAppEndsAtARunningLabelledContainerAndCutsOverNothing(t *testing.T) {
	t.Parallel()

	machine := &box{}
	started, err := over(machine).ProvisionContainers(context.Background(), aStack(t, anApp()), nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if len(started) != 1 {
		t.Fatalf("ProvisionContainers() started %v, want the one app the spec names", started)
	}
	container := started[0]
	if container.Name != "web" {
		t.Errorf("the container is recorded under %q, want the app's own name", container.Name)
	}
	if container.Physical == "" || !strings.Contains(container.URL, container.Physical+":"+containerimage.PortText) {
		t.Errorf("the container is reachable at %q, want the name and port the proxy dials it by", container.URL)
	}
	joined := strings.Join(machine.commands(), "\n")
	if strings.Contains(joined, host.SwitchboardMounted) || strings.Contains(joined, host.ProxyConfig) {
		t.Errorf("starting a container reached the proxy:\n%s\nreleases end at a running container, and the cutover is a separate call", joined)
	}
}

func TestTwoReleasesOfOneAppNeverShareAContainerName(t *testing.T) {
	t.Parallel()

	first, err := over(&box{}).ProvisionContainers(context.Background(), aStack(t, anApp()), nil)
	if err != nil {
		t.Fatal(err)
	}
	next := anApp()
	next.BuildID = "fedcba9876543210fedcba9876543210"
	second, err := over(&box{}).ProvisionContainers(context.Background(), aStack(t, next), nil)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Physical == second[0].Physical {
		t.Errorf("two releases started as %q, and the drain counts in-flight requests per dial address", first[0].Physical)
	}
}

func findingHealthPath(found session.Result) *box {
	return &box{refuses: func(command string) (session.Result, bool) {
		if strings.Contains(command, "find-health-path") {
			return found, true
		}
		return session.Result{}, false
	}}
}

func TestAnAppWithNoHealthPathRecordsThePathTheBoxFoundForItBeforeItIsPromoted(t *testing.T) {
	t.Parallel()

	pathless := anApp()
	pathless.HealthCheckPath = ""
	machine := findingHealthPath(session.Result{Stdout: switchboard.Answered + " /up 404\n" + switchboard.Answered + " /health 200\n"})
	started, err := over(machine).ProvisionContainers(context.Background(), aStack(t, pathless), nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if len(started) != 1 || started[0].DiscoveredHealthCheckPath != "/health" {
		t.Fatalf("ProvisionContainers() = %+v, want the container recorded with the discovered path /health", started)
	}
	found, promoted := -1, -1
	for at, command := range machine.commands() {
		if found < 0 && strings.Contains(command, "find-health-path") {
			found = at
		}
		if promoted < 0 && strings.Contains(command, "promote") {
			promoted = at
		}
	}
	if found < 0 || promoted >= 0 && promoted < found {
		t.Errorf("the box was asked for a health path at %d and the image promoted at %d, want the path found first: %v", found, promoted, machine.commands())
	}
}

func TestAnAppWhoseEveryProbedPathIsAbsentIsRefusedWithHowToNameOne(t *testing.T) {
	t.Parallel()

	pathless := anApp()
	pathless.HealthCheckPath = ""
	var said strings.Builder
	for _, path := range []string{"/up", "/health", "/healthz", "/"} {
		said.WriteString(switchboard.Answered + " " + path + " 404\n")
	}
	_, err := over(findingHealthPath(session.Result{Code: 6, Stdout: said.String()})).ProvisionContainers(context.Background(), aStack(t, pathless), nil)
	if err == nil || !strings.Contains(err.Error(), "health.path") {
		t.Fatalf("ProvisionContainers() = %v, want a refusal naming health.path", err)
	}
}

func TestAnAppWithAPathAnEarlierReleaseDiscoveredIsGatedOnItWithNoProbing(t *testing.T) {
	t.Parallel()

	pinned := anApp()
	pinned.HealthCheckPath = ""
	pinned.DiscoveredHealthCheckPath = "/healthz"
	machine := findingHealthPath(session.Result{Code: 1})
	started, err := over(machine).ProvisionContainers(context.Background(), aStack(t, pinned), nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if started[0].DiscoveredHealthCheckPath != "/healthz" {
		t.Errorf("the container is recorded with discovered path %q, want /healthz kept", started[0].DiscoveredHealthCheckPath)
	}
	for _, command := range machine.commands() {
		if strings.Contains(command, "find-health-path") {
			t.Errorf("a release with a discovered path probed for another: %s", command)
		}
	}
}

func TestAnAppNamingAHealthPathRecordsNoneDiscovered(t *testing.T) {
	t.Parallel()

	named := anApp()
	named.DiscoveredHealthCheckPath = "/up"
	started, err := over(&box{}).ProvisionContainers(context.Background(), aStack(t, named), nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if started[0].DiscoveredHealthCheckPath != "" {
		t.Errorf("an app naming /healthz is recorded with discovered path %q, want none: a path the config names always wins", started[0].DiscoveredHealthCheckPath)
	}
}

func TestARemovedStackTakesItsContainersWithIt(t *testing.T) {
	t.Parallel()

	machine := &box{}
	err := over(machine).RemoveContainers(context.Background(), provider.StackRef{},
		[]provider.AppContainer{{Name: "web", Physical: "shop-prod-web-01234567"}}, nil)
	if err != nil {
		t.Fatalf("RemoveContainers() = %v", err)
	}
	joined := strings.Join(machine.commands(), "\n")
	if !strings.Contains(joined, "docker rm") || !strings.Contains(joined, "shop-prod-web-01234567") {
		t.Errorf("a destroy ran %q and left the container running", joined)
	}
}
