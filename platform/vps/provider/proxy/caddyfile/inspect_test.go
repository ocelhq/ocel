package caddyfile_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddyfile"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

var coolifyServed = []string{"ocel-edge-probe.preview.p1305.test", "shop.p1305.test"}

func summed(rendered []byte) string {
	sum := sha256.Sum256(rendered)
	return hex.EncodeToString(sum[:])
}

func servingCoolify(t *testing.T) (*box, caddyfile.Caddyfile) {
	t.Helper()
	machine, front := coolifys(t)
	machine.claimed = coolifyServed
	rendered, err := front.Render(proxy.Spec{Hostnames: coolifyServed})
	if err != nil {
		t.Fatal(err)
	}
	machine.placed = summed(rendered)
	machine.said["docker inspect"] = "coolify ocel \n"
	machine.answers = map[string]router.Kind{}
	for _, hostname := range coolifyServed {
		machine.answers[hostname] = switchboard.RouterKind
	}
	return machine, front
}

func checked(t *testing.T, front caddyfile.Caddyfile) map[string]provider.HostCheck {
	t.Helper()
	inspected, err := front.Inspect(context.Background())
	if err != nil {
		t.Fatalf("Inspect() = %v", err)
	}
	bySubject := map[string]provider.HostCheck{}
	for _, check := range inspected {
		bySubject[check.Subject] = check
	}
	return bySubject
}

func TestInspectPassesWhenYourCaddyServesWhatOcelPlacedAndNothingOfYoursCollides(t *testing.T) {
	t.Parallel()

	_, front := servingCoolify(t)
	checks := checked(t, front)
	for _, subject := range []string{
		"your Caddy (container coolify-proxy) admin endpoint",
		"import of /data/coolify/proxy/caddy/dynamic/ocel.caddy",
		"/data/coolify/proxy/caddy/dynamic/ocel.caddy",
		"ocel-switchboard on coolify",
		"shop.p1305.test",
		"ocel-edge-probe.preview.p1305.test",
		"hostnames your Caddy also serves",
	} {
		check, ok := checks[subject]
		if !ok {
			t.Errorf("Inspect() checked %v, want %q among them", checks, subject)
			continue
		}
		if check.Verdict != provider.HostPass {
			t.Errorf("%s = %v: %s, want it to pass", subject, check.Verdict, check.Finding)
		}
	}
}

func TestInspectFailsWhenTheRunningConfigLacksOcelsHostnamesBecauseTheImportIsNotInEffect(t *testing.T) {
	t.Parallel()

	machine, front := servingCoolify(t)
	machine.claimed = append(slices.Clone(coolifyServed), "blog.p1305.test")
	check := checked(t, front)["import of /data/coolify/proxy/caddy/dynamic/ocel.caddy"]
	if check.Verdict != provider.HostFail || !strings.Contains(check.Finding, "blog.p1305.test") || !strings.Contains(check.Fix, "import /data/coolify/proxy/caddy/dynamic/*.caddy") {
		t.Errorf("import = %+v, want it failed naming blog.p1305.test and the import line to add", check)
	}
}

func TestInspectFailsWhenThePlacedFileIsNotWhatOcelRenders(t *testing.T) {
	t.Parallel()

	machine, front := servingCoolify(t)
	machine.placed = ""
	check := checked(t, front)["/data/coolify/proxy/caddy/dynamic/ocel.caddy"]
	if check.Verdict != provider.HostFail || !strings.Contains(check.Fix, "deploy") {
		t.Errorf("the placed file = %+v, want it failed with a deploy, which places it again, as the fix", check)
	}
}

func TestInspectFailsWhenTheSwitchboardIsOffYourCaddysNetwork(t *testing.T) {
	t.Parallel()

	machine, front := servingCoolify(t)
	machine.said["docker inspect"] = "ocel \n"
	check := checked(t, front)["ocel-switchboard on coolify"]
	if check.Verdict != provider.HostFail {
		t.Errorf("ocel-switchboard on coolify = %+v, want it failed: your Caddy reaches it by name there", check)
	}
}

func TestInspectFailsAHostnameThatDoesNotAnswerThroughTheSwitchboard(t *testing.T) {
	t.Parallel()

	machine, front := servingCoolify(t)
	machine.answers["shop.p1305.test"] = ""
	machine.failures = map[string]string{"shop.p1305.test": "shop.p1305.test answered nothing over tls at 127.0.0.1:443"}
	check := checked(t, front)["shop.p1305.test"]
	if check.Verdict != provider.HostFail || !strings.Contains(check.Finding, "answered nothing") {
		t.Errorf("shop.p1305.test = %+v, want it failed with what the probe met", check)
	}
}

func TestInspectNamesASiteOfYoursAddedLaterThatNowCollidesWithAHostnameOcelServes(t *testing.T) {
	t.Parallel()

	machine, front := servingCoolify(t)
	machine.claimed = append(slices.Clone(coolifyServed), "web.p1305.test")
	check := checked(t, front)["hostnames your Caddy also serves"]
	if check.Verdict != provider.HostFail || !strings.Contains(check.Finding, "web.p1305.test") || !strings.Contains(check.Finding, "srv0") {
		t.Errorf("collisions = %+v, want it failed naming web.p1305.test and where your Caddy serves it", check)
	}
}

func TestInspectStopsAtAnAdminEndpointThatDoesNotAnswer(t *testing.T) {
	t.Parallel()

	machine, front := servingCoolify(t)
	machine.refused = map[string]string{caddyfile.AdminServers: "wget: can't connect to remote host: Connection refused"}
	checks := checked(t, front)
	check := checks["your Caddy (container coolify-proxy) admin endpoint"]
	if check.Verdict != provider.HostFail || !strings.Contains(check.Finding, "Connection refused") {
		t.Errorf("admin endpoint = %+v, want it failed with what the read met", check)
	}
}

func TestInspectDoesNotTakeASiteOfYoursProxyingToTheSwitchboardForOcelsImport(t *testing.T) {
	t.Parallel()

	machine, front := servingCoolify(t)
	machine.said[caddyfile.AdminServers] = sameUpstream(append(slices.Clone(coolifyServed), "mine.p1305.test"))
	checks := checked(t, front)
	if check := checks["import of /data/coolify/proxy/caddy/dynamic/ocel.caddy"]; check.Verdict != provider.HostFail {
		t.Errorf("import = %+v, want it failed: the only route to the switchboard is a site of yours that serves more than ocel placed", check)
	}
	if check := checks["hostnames your Caddy also serves"]; check.Verdict != provider.HostFail || !strings.Contains(check.Finding, "shop.p1305.test") {
		t.Errorf("collisions = %+v, want it failed naming shop.p1305.test, which a site of yours serves", check)
	}
}
