package vps_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/envsource"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const (
	fakeInfisicalUnit   = "ocel-live-fake-infisical"
	fakeInfisicalScript = "/var/tmp/ocel-live-fake-infisical.py"
	fakeInfisicalSecret = "/var/tmp/ocel-live-fake-infisical.secret"
	fakeInfisicalHost   = "http://127.0.0.1:18080"
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
        held = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))) or b"{}")
        if self.path != "/api/v1/auth/universal-auth/login":
            return self.answer(404, {})
        if held.get("clientId") != "id" or held.get("clientSecret") != "secret":
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

func (vm machine) standsFakeInfisical(t *testing.T) {
	t.Helper()
	vm.feeds(t, "sudo tee "+fakeInfisicalScript+" >/dev/null", []byte(fakeInfisical))
	vm.holdsInInfisical(t, "postgres://first", 1)
	vm.ssh(t, "sudo systemctl stop "+fakeInfisicalUnit+" >/dev/null 2>&1 || true")
	vm.ssh(t, "sudo systemd-run --collect --unit="+fakeInfisicalUnit+" python3 "+fakeInfisicalScript+" >/dev/null")
	t.Cleanup(func() {
		vm.ssh(t, "sudo systemctl stop "+fakeInfisicalUnit+" >/dev/null 2>&1 || true")
		vm.ssh(t, "sudo rm -f "+fakeInfisicalScript+" "+fakeInfisicalSecret)
	})
}

func (vm machine) holdsInInfisical(t *testing.T, value string, version int) {
	t.Helper()
	vm.feeds(t, "sudo tee "+fakeInfisicalSecret+" >/dev/null", []byte(value+"\n"+strings.Repeat("1", version)+"\n"))
}

func mirrored(ctx context.Context, store values.Store, scope values.Scope, want string, within time.Duration) (values.Value, error) {
	deadline := time.Now().Add(within)
	for {
		held, err := store.Get(ctx, scope, values.Coordinate{Cell: values.Cell{Key: "DATABASE_URL"}}, true)
		if err == nil && held.Plaintext == want {
			return held, nil
		}
		if err != nil && !errors.Is(err, values.ErrNotFound) {
			return values.Value{}, err
		}
		if time.Now().After(deadline) {
			return held, errors.New("never mirrored")
		}
		time.Sleep(2 * time.Second)
	}
}

func TestLiveTheSyncerKeepsAClassesValuesInStepWithItsSourceAndGoesWithTheClass(t *testing.T) {
	vm := live(t)
	vm.purges(t)
	dirties(t, vm)
	class := providerkit.ClassProduction
	p := bootstrapped(t, vm, class)
	ctx := context.Background()
	service := host.EnvSyncService(class)

	if active := strings.TrimSpace(vm.ssh(t, "systemctl is-active "+service+" || true")); active != "active" {
		t.Fatalf("%s is %q after a bootstrap, want the syncer running before any deploy registers a source", service, active)
	}

	vm.standsFakeInfisical(t)
	store := values.Store{Records: p.Records(), Sealer: p.Sealer()}
	scope := values.Scope{Project: "shop", Class: class}
	for key, value := range map[string]string{"INFISICAL_CLIENT_ID": "id", "INFISICAL_CLIENT_SECRET": "secret"} {
		if _, err := store.Set(ctx, scope, values.Coordinate{Cell: values.Cell{Key: key}}, value, nil); err != nil {
			t.Fatalf("Set(%s) = %v", key, err)
		}
	}
	registration := envsource.Registration{
		Project: "shop",
		Folders: []string{""},
		Descriptor: envsource.Descriptor{Kind: envsource.Infisical, Infisical: &envsource.InfisicalOptions{
			Project: "p-1", Environment: "prod", Path: "/", Host: fakeInfisicalHost, Write: envsource.WriteNever,
			Auth: envsource.InfisicalAuth{Method: envsource.AuthUniversal, ClientIDVar: "INFISICAL_CLIENT_ID", ClientSecretVar: "INFISICAL_CLIENT_SECRET"},
		}},
	}
	if err := envsource.Register(ctx, store.Records, class, registration); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	vm.ssh(t, "sudo systemctl restart "+service)

	held, err := mirrored(ctx, store, scope, "postgres://first", time.Minute)
	if err != nil {
		t.Fatalf("DATABASE_URL reads %q a minute after the syncer started over a registered source (%v):\n%s",
			held.Plaintext, err, vm.ssh(t, "sudo journalctl -u "+service+" --no-pager -n 30"))
	}
	if held.Provenance.EnvSource != "infisical:p-1/prod" {
		t.Errorf("DATABASE_URL was written with provenance %+v, want the source that owns it", held.Provenance)
	}

	vm.holdsInInfisical(t, "postgres://second", 2)
	if held, err := mirrored(ctx, store, scope, "postgres://second", 2*envsource.PollInterval); err != nil {
		t.Fatalf("DATABASE_URL still reads %q two poll intervals after the source changed it (%v):\n%s",
			held.Plaintext, err, vm.ssh(t, "sudo journalctl -u "+service+" --no-pager -n 30"))
	}
	status, err := envsource.StatusOf(ctx, store, class, registration)
	if err != nil || status.LastSuccessAt == 0 || status.LastError != "" {
		t.Errorf("StatusOf() = %+v, %v, want a success the syncer recorded", status, err)
	}

	bootstrapper, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrapper.Remove(ctx, class, nil); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if said := strings.TrimSpace(vm.ssh(t, "systemctl cat "+service+" >/dev/null 2>&1 && echo standing || echo gone")); said != "gone" {
		t.Errorf("%s is %s after the last class went", service, said)
	}
	for _, gone := range []string{"/etc/systemd/system/ocel-envsync@.service", host.LiveBinary, host.StateDir(class)} {
		if vm.stands(t, gone) {
			t.Errorf("%s stands after the last class went", gone)
		}
	}
}
