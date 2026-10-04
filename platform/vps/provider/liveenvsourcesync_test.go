//go:build integration

package vps_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const (
	fakeInfisicalUnit   = "ocel-live-fake-infisical"
	fakeInfisicalScript = "/var/tmp/ocel-live-fake-infisical.py"
	fakeInfisicalSecret = "/var/tmp/ocel-live-fake-infisical.secret"
	fakeInfisicalHost   = "http://127.0.0.1:18080"
	syncWithinTwoPolls  = 2*time.Minute + 30*time.Second
)

const fakeInfisical = `import json
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import urlparse, parse_qs

SECRET = "` + fakeInfisicalSecret + `"


class Handler(BaseHTTPRequestHandler):
    def answer(self, status, body):
        raw = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_POST(self):
        sent = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))) or b"{}")
        if self.path != "/api/v1/auth/universal-auth/login":
            return self.answer(404, {})
        if sent.get("clientId") != "id" or sent.get("clientSecret") != "secret":
            return self.answer(401, {"message": "Invalid credentials"})
        self.answer(200, {"accessToken": "token", "expiresIn": 3600})

    def do_GET(self):
        if self.headers.get("Authorization") != "Bearer token":
            return self.answer(401, {})
        at = urlparse(self.path)
        if at.path == "/api/v1/projects/p-1":
            return self.answer(200, {"project": {"orgId": "org-1"}})
        if at.path != "/api/v4/secrets":
            return self.answer(404, {})
        if parse_qs(at.query).get("secretPath") != ["/"]:
            return self.answer(200, {"secrets": []})
        with open(SECRET) as f:
            value, version = f.read().split("\n")[:2]
        self.answer(200, {"secrets": [{"id": "s-1", "secretKey": "DATABASE_URL", "secretValue": value, "version": int(version)}]})


HTTPServer(("127.0.0.1", 18080), Handler).serve_forever()
`

func (vm machine) startsFakeInfisical(t *testing.T) {
	t.Helper()
	vm.feeds(t, "sudo tee "+fakeInfisicalScript+" >/dev/null", []byte(fakeInfisical))
	vm.setsInInfisical(t, "postgres://first", 1)
	vm.ssh(t, "sudo systemctl stop "+fakeInfisicalUnit+" >/dev/null 2>&1 || true")
	vm.ssh(t, "sudo systemd-run --collect --unit="+fakeInfisicalUnit+" python3 "+fakeInfisicalScript+" >/dev/null")
	t.Cleanup(func() {
		vm.ssh(t, "sudo systemctl stop "+fakeInfisicalUnit+" >/dev/null 2>&1 || true")
		vm.ssh(t, "sudo rm -f "+fakeInfisicalScript+" "+fakeInfisicalSecret)
	})
}

func (vm machine) setsInInfisical(t *testing.T, value string, version int) {
	t.Helper()
	vm.feeds(t, "sudo tee "+fakeInfisicalSecret+" >/dev/null", []byte(value+"\n"+strings.Repeat("1", version)+"\n"))
}

func copied(ctx context.Context, store variablestore.Store, scope variablestore.Scope, want string, within time.Duration) (variablestore.Value, error) {
	deadline := time.Now().Add(within)
	for {
		found, err := store.Get(ctx, scope, variablestore.Coordinate{Cell: variablestore.Cell{Key: "DATABASE_URL"}}, true)
		if err == nil && found.Plaintext == want {
			return found, nil
		}
		if err != nil && !errors.Is(err, variablestore.ErrNotFound) {
			return variablestore.Value{}, err
		}
		if time.Now().After(deadline) {
			return found, errors.New("never copied")
		}
		time.Sleep(2 * time.Second)
	}
}

func TestLiveTheEnvSourceSyncKeepsATiersValuesInStepWithItsEnvSourceAndGoesWithTheTier(t *testing.T) {
	vm := liveMachine(t)
	vm.purges(t)
	dirties(t, vm)
	tier := environment.TierProduction
	p := bootstrapped(t, vm, tier)
	ctx := context.Background()
	service := host.EnvSourceSyncService(tier)

	if active := strings.TrimSpace(vm.ssh(t, "systemctl is-active "+service+" || true")); active != "active" {
		t.Fatalf("%s is %q after a bootstrap, want the sync running before any deploy registers an env source", service, active)
	}

	vm.startsFakeInfisical(t)
	store := variablestore.Store{KeyValues: p.KeyValues(), Cipher: p.Cipher()}
	scope := variablestore.Scope{Project: "shop", Tier: tier}
	for key, value := range map[string]string{"INFISICAL_CLIENT_ID": "id", "INFISICAL_CLIENT_SECRET": "secret"} {
		if _, err := store.Set(ctx, scope, variablestore.Coordinate{Cell: variablestore.Cell{Key: key}}, value, nil); err != nil {
			t.Fatalf("Set(%s) = %v", key, err)
		}
	}
	registration := envsource.Registration{
		Project: "shop",
		Folders: []string{""},
	}
	descriptor, err := envsource.NewDescriptor("infisical", []byte(`{"project":"p-1","environment":"prod","path":"/","host":"`+fakeInfisicalHost+`","write":"never","auth":{"universal":{"clientId":{"$env":"INFISICAL_CLIENT_ID"},"clientSecret":{"$env":"INFISICAL_CLIENT_SECRET"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	registration.Descriptor = descriptor
	if _, err := envsource.Register(ctx, store, tier, registration); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	vm.ssh(t, "sudo systemctl restart "+service)

	found, err := copied(ctx, store, scope, "postgres://first", time.Minute)
	if err != nil {
		t.Fatalf("DATABASE_URL reads %q a minute after the sync started over a registered env source (%v):\n%s",
			found.Plaintext, err, vm.ssh(t, "sudo journalctl -u "+service+" --no-pager -n 30"))
	}
	if found.Provenance.EnvSource != "infisical:p-1/prod" {
		t.Errorf("DATABASE_URL was written with provenance %+v, want the env source that owns it", found.Provenance)
	}

	vm.setsInInfisical(t, "postgres://second", 2)
	if found, err := copied(ctx, store, scope, "postgres://second", syncWithinTwoPolls); err != nil {
		t.Fatalf("DATABASE_URL still reads %q two polls after the env source changed it (%v):\n%s",
			found.Plaintext, err, vm.ssh(t, "sudo journalctl -u "+service+" --no-pager -n 30"))
	}
	status, err := envsource.StatusOf(ctx, store, tier, registration)
	if err != nil || status.LastSuccessAt.IsZero() || status.LastError != "" {
		t.Errorf("StatusOf() = %+v, %v, want a success the sync recorded", status, err)
	}

	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Remove(ctx, tier, nil); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if said := strings.TrimSpace(vm.ssh(t, "systemctl cat "+service+" >/dev/null 2>&1 && echo present || echo gone")); said != "gone" {
		t.Errorf("%s is %s after the last tier went", service, said)
	}
	for _, gone := range []string{"/etc/systemd/system/ocel-envsourcesync@.service", host.LiveBinary, host.StateDir(tier)} {
		if vm.exists(t, gone) {
			t.Errorf("%s is present after the last tier went", gone)
		}
	}
}
