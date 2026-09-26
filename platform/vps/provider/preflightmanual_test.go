package vps_test

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

const manualRecord = `{"proxy":{"manual":{"port":8480}},"project":"shop","class":"production"}`

func routedByHand(overrides map[string]answer) *scripted {
	held := map[string]answer{
		"proxy.json":                {stdout: manualRecord},
		"publish=" + caddy.HTTPPort: {stdout: "\n"},
		"publish=443":               {stdout: "\n"},
		"cat /proc/net/tcp\n":       {stdout: socketTable(80, 443)},
		"'test' '-S'":               {code: 1, stderr: "Error: No such container: " + caddy.Container},
		"flock -s 9":                {stdout: "+" + base64.StdEncoding.EncodeToString([]byte(`{"grace":"30s"}`)) + "\n\n"},
	}
	for naming, said := range overrides {
		held[naming] = said
	}
	return boxSaying(held)
}

func preflightingByHand(machine *scripted) error {
	p := vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}, Proxy: &vps.Proxy{Manual: &vps.Manual{}}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
	stack, err := naming.ParseStackName("prod--web--r0a1b2c3d")
	if err != nil {
		return err
	}
	return p.PreflightDeploy(context.Background(), providerkit.DeployPreflight{
		Plan: providerkit.DeployPlan{
			Slug:  "shop",
			Class: edge.ClassProduction,
			Apps:  []providerkit.AppEntry{{App: "web", Stack: stack, Image: deployedRef}},
		},
	})
}

func TestABoxYourProxyFrontsIsReadyWithNoProxyOfOcelsOwn(t *testing.T) {
	t.Parallel()

	machine := routedByHand(nil)
	if err := preflightingByHand(machine); err != nil {
		t.Fatalf("PreflightDeploy() = %v, want a box whose own proxy holds 80 and 443 let through", err)
	}
	for _, command := range machine.ran {
		if strings.Contains(command, "'test' '-S'") {
			t.Errorf("the preflight asked after %s's admin socket on a box that runs no %s: %s", caddy.Container, caddy.Container, command)
		}
	}
}

func TestABoxYourProxyFrontsRefusesAServingPortNothingHolds(t *testing.T) {
	t.Parallel()

	err := preflightingByHand(routedByHand(map[string]answer{"cat /proc/net/tcp\n": {stdout: socketTable(80)}}))
	if err == nil {
		t.Fatal("PreflightDeploy() let a deploy onto a box where nothing holds 443, and nothing would reach what it serves")
	}
	if !strings.Contains(err.Error(), "nothing listens on 443") || !strings.Contains(err.Error(), "your proxy") {
		t.Errorf("PreflightDeploy() = %q, want 443 named as the port your proxy must hold", err)
	}
}

func TestABoxYourProxyFrontsRefusesOcelsOwnProxyOnAServingPort(t *testing.T) {
	t.Parallel()

	err := preflightingByHand(routedByHand(map[string]answer{"publish=443": {stdout: caddy.Container + "\n"}}))
	if err == nil {
		t.Fatalf("PreflightDeploy() let a deploy past %s holding 443 on a box your proxy fronts", caddy.Container)
	}
	if !strings.Contains(err.Error(), caddy.Container) {
		t.Errorf("PreflightDeploy() = %q, want %s named", err, caddy.Container)
	}
}

func TestABoxOcelsOwnProxyFrontsRefusesAProjectThatRoutesByHand(t *testing.T) {
	t.Parallel()

	err := preflightingByHand(routedByHand(map[string]answer{"proxy.json": {stdout: builtInRecord}}))
	if err == nil {
		t.Fatal("PreflightDeploy() let a project that routes by hand deploy onto a box ocel's own proxy fronts")
	}
	for _, wanted := range []string{"ocel's own proxy", "set by shop/production", "remove `\"proxy\"`"} {
		if !strings.Contains(err.Error(), wanted) {
			t.Errorf("PreflightDeploy() = %q, want %q in it", err, wanted)
		}
	}
}
