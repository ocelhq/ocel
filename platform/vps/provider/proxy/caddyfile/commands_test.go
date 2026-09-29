package caddyfile_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddyfile"
)

func TestTheSnippetIsAdaptedAloneInsideYourCaddysContainerBeforeItIsPlaced(t *testing.T) {
	t.Parallel()

	machine := &box{}
	rendered := []byte(golden(t, "network.caddy"))
	front := caddyfile.Caddyfile{Box: machine, Container: "caddy", Config: "/etc/caddy/Caddyfile", Network: "web"}
	if err := front.Validate(context.Background(), rendered); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	want := []string{"docker", "exec", "-i", "caddy", "caddy", "adapt", "--config", "/dev/stdin", "--adapter", "caddyfile"}
	if len(machine.ran) != 1 || !slices.Equal(machine.ran[0].argv, want) || string(machine.ran[0].stdin) != string(rendered) {
		t.Errorf("Validate() ran %q, want %q fed the snippet", machine.argvs(), want)
	}
}

func TestTheSnippetIsAdaptedByTheHostsCaddyWhenCaddyRunsAsAService(t *testing.T) {
	t.Parallel()

	machine := &box{}
	rendered := []byte(golden(t, "service.caddy"))
	if err := (caddyfile.Caddyfile{Box: machine, Port: 8480}).Validate(context.Background(), rendered); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
	want := []string{"/usr/bin/caddy", "adapt", "--config", "/dev/stdin", "--adapter", "caddyfile"}
	if len(machine.ran) != 1 || !slices.Equal(machine.ran[0].argv, want) || string(machine.ran[0].stdin) != string(rendered) {
		t.Errorf("Validate() ran %q, want %q fed the snippet", machine.argvs(), want)
	}
}

func TestASnippetYourCaddyCannotAdaptIsRefusedWithWhatYourCaddySaid(t *testing.T) {
	t.Parallel()

	said := "Error: /dev/stdin:2: unrecognized directive: bogus"
	machine := &box{refused: map[string]string{"adapt": said}}
	err := (caddyfile.Caddyfile{Box: machine, Container: "coolify-proxy"}).Validate(context.Background(), []byte(golden(t, "network.caddy")))
	if err == nil || !strings.Contains(err.Error(), said) || !strings.Contains(err.Error(), caddyfile.FileName) {
		t.Errorf("Validate() = %v, want it refused naming %s and carrying %q", err, caddyfile.FileName, said)
	}
}

func TestReloadRunsCaddyReloadInsideYourContainerNamingTheConfigAndItsAdapter(t *testing.T) {
	t.Parallel()

	machine := &box{}
	front := caddyfile.Caddyfile{Box: machine, Container: "caddy", Config: "/etc/caddy/Caddyfile"}
	if err := front.Reload(context.Background()); err != nil {
		t.Fatalf("Reload() = %v", err)
	}
	want := [][]string{{"docker", "exec", "caddy", "caddy", "reload", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"}}
	if got := machine.argvs(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("Reload() ran %q, want %q: the official image's workdir is /srv, so the config is always named", got, want)
	}
}

func TestReloadUnderAPresetRunsTheReloadTheHostToolRunsItself(t *testing.T) {
	t.Parallel()

	machine := &box{}
	front := caddyfile.Caddyfile{Box: machine, Preset: "coolify", Container: "coolify-proxy", Config: "/config/caddy/Caddyfile.autosave", Network: "coolify"}
	if err := front.Reload(context.Background()); err != nil {
		t.Fatalf("Reload() = %v", err)
	}
	want := [][]string{{"docker", "exec", "coolify-proxy", "caddy", "reload", "--config", "/config/caddy/Caddyfile.autosave"}}
	if got := machine.argvs(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("Reload() ran %q, want Coolify's own %q: caddy-docker-proxy takes up a new ocel.caddy only on a reload", got, want)
	}
}

func TestReloadOfACaddyServiceGoesThroughTheOneSudoLineBootstrapWrites(t *testing.T) {
	t.Parallel()

	machine := &box{}
	if err := (caddyfile.Caddyfile{Box: machine, Port: 8480, Config: "/etc/caddy/Caddyfile"}).Reload(context.Background()); err != nil {
		t.Fatalf("Reload() = %v", err)
	}
	want := [][]string{{"sudo", "-n", "/usr/bin/systemctl", "reload", "caddy.service"}}
	if got := machine.argvs(); !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("Reload() ran %q, want %q: the unit reloads in Caddy's own environment, where its {$VAR} placeholders resolve", got, want)
	}
	if granted := caddyfile.ServiceReload(); !slices.Equal(granted, want[0][2:]) {
		t.Errorf("the command sudo is granted for is %q, want exactly the one reload runs, %q", granted, want[0][2:])
	}
}

func TestAReloadYourCaddyRefusesCarriesItsErrorVerbatim(t *testing.T) {
	t.Parallel()

	said := `Error: sending configuration to instance: caddy responded with error: HTTP 400: {"error":"loading config: ambiguous site definition: shop.example.com"}`
	machine := &box{refused: map[string]string{"reload": said}}
	err := (caddyfile.Caddyfile{Box: machine, Container: "caddy", Config: "/etc/caddy/Caddyfile"}).Reload(context.Background())
	if err == nil || !strings.Contains(err.Error(), said) {
		t.Fatalf("Reload() = %v, want it refused carrying %q", err, said)
	}
	if !strings.Contains(err.Error(), "your own Caddyfile") {
		t.Errorf("Reload() = %v, want it to say the error can be in your own Caddyfile", err)
	}
}
