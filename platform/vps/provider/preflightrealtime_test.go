package vps_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func gatewaySurveyed(t *testing.T, content []byte) answer {
	t.Helper()
	sum := sha256.Sum256(content)
	return answer{stdout: machine.KindFile + "\t" + host.RealtimeBinary + "\t755\troot\t" + hex.EncodeToString(sum[:]) + "\n"}
}

func currentGateway(t *testing.T) []byte {
	t.Helper()
	for _, item := range host.RealtimeItems(ssh.AMD64) {
		if item.Name == host.RealtimeBinary {
			return item.Content
		}
	}
	t.Fatal("no realtime item places the gateway")
	return nil
}

var aDeclaredRealtime = provider.Resource{Name: "realtime--app", Declared: "app", Type: provider.BindingRealtime}

func TestADeployDeclaringRealtimeIsRefusedOnABoxWhoseGatewayPredatesThisBuild(t *testing.T) {
	t.Parallel()

	machine := boxSaying(map[string]answer{
		"uname -m":          {stdout: "x86_64\n"},
		host.RealtimeBinary: gatewaySurveyed(t, []byte("a gateway an older ocel placed")),
	})
	err := preflightingResources(t, machine, aDeclaredRealtime)
	if err == nil {
		t.Fatal("ApplyPreflight() let realtime onto a box whose gateway this build never placed, and its gateway would not start")
	}
	if !strings.Contains(err.Error(), "ocel bootstrap") {
		t.Errorf("ApplyPreflight() = %q, want it to name `ocel bootstrap` as the remedy", err)
	}
}

func TestADeployDeclaringRealtimeIsLetOntoABoxRunningThisBuildsGateway(t *testing.T) {
	t.Parallel()

	machine := boxSaying(map[string]answer{
		"uname -m":          {stdout: "x86_64\n"},
		host.RealtimeBinary: gatewaySurveyed(t, currentGateway(t)),
	})
	if err := preflightingResources(t, machine, aDeclaredRealtime); err != nil {
		t.Fatalf("ApplyPreflight() = %v, want a box running this build's gateway let through", err)
	}
}

func TestADeployDeclaringNoRealtimeNeverAsksAboutTheGateway(t *testing.T) {
	t.Parallel()

	machine := boxSaying(map[string]answer{"uname -m": {stdout: "x86_64\n"}})
	if err := preflightingResources(t, machine); err != nil {
		t.Fatalf("ApplyPreflight() = %v", err)
	}
	for _, command := range machine.ran {
		if strings.Contains(command, host.RealtimeBinary) {
			t.Errorf("a deploy with no realtime asked about the gateway: %q", command)
		}
	}
}
