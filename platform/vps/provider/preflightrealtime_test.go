package vps_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func gatewaySurveyed(t *testing.T, content []byte) answer {
	t.Helper()
	sum := sha256.Sum256(content)
	return answer{stdout: host.KindFile + "\t" + host.RealtimeBinary + "\t755\troot\t" + hex.EncodeToString(sum[:]) + "\n"}
}

func currentGateway(t *testing.T) []byte {
	t.Helper()
	for _, item := range host.RealtimeItems(host.ArchAMD64) {
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
		t.Fatal("PreflightDeploy() let realtime onto a box whose gateway this build never placed, and its gateway would not start")
	}
	if !strings.Contains(err.Error(), "ocel bootstrap") {
		t.Errorf("PreflightDeploy() = %q, want it to name `ocel bootstrap` as the remedy", err)
	}
}

func TestADeployDeclaringRealtimeIsLetOntoABoxRunningThisBuildsGateway(t *testing.T) {
	t.Parallel()

	machine := boxSaying(map[string]answer{
		"uname -m":          {stdout: "x86_64\n"},
		host.RealtimeBinary: gatewaySurveyed(t, currentGateway(t)),
	})
	if err := preflightingResources(t, machine, aDeclaredRealtime); err != nil {
		t.Fatalf("PreflightDeploy() = %v, want a box running this build's gateway let through", err)
	}
}

func TestADeployDeclaringNoRealtimeNeverAsksAboutTheGateway(t *testing.T) {
	t.Parallel()

	machine := boxSaying(map[string]answer{"uname -m": {stdout: "x86_64\n"}})
	if err := preflightingResources(t, machine); err != nil {
		t.Fatalf("PreflightDeploy() = %v", err)
	}
	for _, command := range machine.ran {
		if strings.Contains(command, host.RealtimeBinary) {
			t.Errorf("a deploy with no realtime asked about the gateway: %q", command)
		}
	}
}

func TestAnEphemeralPreviewDeclaringRealtimeIsRefusedBeforeTheBoxIsAsked(t *testing.T) {
	t.Parallel()

	machine := boxSaying(map[string]answer{
		"uname -m":          {stdout: "x86_64\n"},
		host.RealtimeBinary: gatewaySurveyed(t, currentGateway(t)),
	})
	err := preflightingIn(t, machine, naming.StackName{}, aDeclaredRealtime)
	if err == nil {
		t.Fatal("PreflightDeploy() let an ephemeral preview declare realtime, and it has no gateway of its own to serve it")
	}
	for _, want := range []string{"app", "ephemeral"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("PreflightDeploy() = %q, want it to name %s", err, want)
		}
	}
	if len(machine.ran) != 0 {
		t.Errorf("PreflightDeploy() ran %v on the box before refusing what the config asks for", machine.ran)
	}
}
