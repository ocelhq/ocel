package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const deployment = "0123456789abcdef0123456789abcdef"

func aStack(t *testing.T, app provider.AppSpec) provider.StackSpec {
	t.Helper()
	stack, err := naming.ParseStackName("prod--web--r0a1b2c3d")
	if err != nil {
		t.Fatal(err)
	}
	return provider.StackSpec{
		Ref:  provider.StackRef{Project: "shop", Class: edge.ClassProduction, Name: stack},
		Kind: provider.StackApp,
		App:  &app,
	}
}

func anApp() provider.AppSpec {
	return provider.AppSpec{
		App:             "web",
		Compute:         provider.ComputeContainer,
		Deployment:      deployment,
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

func TestStandingAnAppUpEndsAtARunningLabelledContainerAndFlipsNothing(t *testing.T) {
	t.Parallel()

	machine := &box{}
	standing, err := over(machine).ProvisionContainers(context.Background(), aStack(t, anApp()), nil)
	if err != nil {
		t.Fatalf("ProvisionContainers() = %v", err)
	}
	if len(standing) != 1 {
		t.Fatalf("ProvisionContainers() stood up %v, want the one app the spec carries", standing)
	}
	held := standing[0]
	if held.Name != "web" {
		t.Errorf("the container is recorded under %q, want the app's own name", held.Name)
	}
	if held.Physical == "" || !strings.Contains(held.URL, held.Physical+":"+appbuild.InjectedPortText) {
		t.Errorf("the container is reachable at %q, want the name and port the proxy dials it by", held.URL)
	}
	joined := strings.Join(machine.commands(), "\n")
	if strings.Contains(joined, host.SwitchboardMounted) || strings.Contains(joined, host.ProxyConfig) {
		t.Errorf("standing a container up reached the proxy:\n%s\nreleases end at a running container, and the flip is a separate call", joined)
	}
}

func TestTwoReleasesOfOneAppNeverShareAContainerName(t *testing.T) {
	t.Parallel()

	first, err := over(&box{}).ProvisionContainers(context.Background(), aStack(t, anApp()), nil)
	if err != nil {
		t.Fatal(err)
	}
	next := anApp()
	next.Deployment = "fedcba9876543210fedcba9876543210"
	second, err := over(&box{}).ProvisionContainers(context.Background(), aStack(t, next), nil)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].Physical == second[0].Physical {
		t.Errorf("two releases stood up as %q, and the drain counts in-flight requests per dial address", first[0].Physical)
	}
}

func TestAnAppWithNoHealthPathIsRefusedRatherThanGivenOneThisProviderChose(t *testing.T) {
	t.Parallel()

	pathless := anApp()
	pathless.HealthCheckPath = ""
	_, err := over(&box{}).ProvisionContainers(context.Background(), aStack(t, pathless), nil)
	if err == nil {
		t.Fatal("an app carrying no health path stood up, and the gate would then probe a path the user was never shown")
	}
	if !strings.Contains(err.Error(), "health") {
		t.Errorf("the refusal reads %q and never names what is missing", err)
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
		t.Errorf("a destroy ran %q and left the container standing", joined)
	}
}
