package caddy_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

type box struct {
	ran       []string
	answer    func(command string) (string, error)
	logs      string
	unreached error
}

func (b *box) Ran(_ context.Context, _ string, argv []string) (string, error) {
	command := strings.Join(argv, " ")
	b.ran = append(b.ran, command)
	if b.answer == nil {
		return "", nil
	}
	return b.answer(command)
}

func (b *box) Said(_ context.Context, argv []string) (string, error) {
	b.ran = append(b.ran, strings.Join(argv, " "))
	return b.logs, b.unreached
}

func TestTheBuiltinProxyIsRenderedIntoTheFileItsContainerMounts(t *testing.T) {
	t.Parallel()

	if got := (caddy.Builtin{}).File(); got != live.ProxyConfig {
		t.Errorf("File() = %q, want %q: it is mounted at %s, the file the proxy runs and reloads", got, live.ProxyConfig, caddy.ConfigMount)
	}
}

func TestAReloadTakesUpTheConfigOnDiskOverTheAdminSocket(t *testing.T) {
	t.Parallel()

	machine := &box{}
	if err := (caddy.Builtin{Box: machine}).Reload(context.Background()); err != nil {
		t.Fatalf("Reload() = %v", err)
	}
	if len(machine.ran) != 1 {
		t.Fatalf("Reload() ran %q, want the one reload", machine.ran)
	}
	for _, wanted := range []string{"docker exec " + caddy.Container + " caddy reload", "--config " + caddy.ConfigMount, "--address unix/" + caddy.AdminSocket} {
		if !strings.Contains(machine.ran[0], wanted) {
			t.Errorf("Reload() ran %q, which has no %s", machine.ran[0], wanted)
		}
	}
	if strings.Contains(machine.ran[0], "--force") {
		t.Errorf("Reload() ran %q: a forced reload restarts every server caddy runs even when nothing changed", machine.ran[0])
	}
}

func TestAReloadTheProxyRefusesIsTheReloadsFailure(t *testing.T) {
	t.Parallel()

	refused := errors.New("adapting config using json: loading tls app: boom")
	machine := &box{answer: func(string) (string, error) { return "", refused }}
	if err := (caddy.Builtin{Box: machine}).Reload(context.Background()); !errors.Is(err, refused) {
		t.Errorf("Reload() = %v, want the refused reload returned", err)
	}
}

const tcpHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

func listened(ports ...string) string {
	written := tcpHeader
	for at, port := range ports {
		written += "   " + string(rune('0'+at)) + ": 00000000:" + port + " 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0000000000000000 100 0 0 10 0\n"
	}
	return written
}

func TestTheChecksReadWhetherTheAdminApiListensOnAPortInsideTheProxy(t *testing.T) {
	t.Parallel()

	for what, check := range map[string]struct {
		said    string
		err     error
		verdict provider.HostVerdict
	}{
		"only the serving ports":          {said: listened("0050", "01BB"), verdict: provider.HostPass},
		"the admin port beside them":      {said: listened("0050", "01BB", "07E3"), verdict: provider.HostFail},
		"nothing at all":                  {said: tcpHeader, verdict: provider.HostFail},
		"a proxy that answered nothing":   {err: errors.New("no such container"), verdict: provider.HostFail},
		"a table that is no socket table": {said: "garbage\n", verdict: provider.HostFail},
	} {
		machine := &box{answer: func(string) (string, error) { return check.said, check.err }}
		checks, err := (caddy.Builtin{Box: machine}).Inspect(context.Background())
		if err != nil {
			t.Fatalf("Inspect() over %s = %v", what, err)
		}
		if len(checks) != 1 || checks[0].Verdict != check.verdict {
			t.Errorf("Inspect() over %s = %+v, want one %v verdict", what, checks, check.verdict)
		}
		if len(machine.ran) != 1 || !strings.HasPrefix(machine.ran[0], "docker exec "+caddy.Container+" ") || !strings.Contains(machine.ran[0], "/proc/net/tcp") {
			t.Errorf("Inspect() over %s ran %q, want the proxy's own socket tables read from inside it", what, machine.ran)
		}
	}
}

func TestTheTroubleWithACertificateIsWhatTheProxyLogged(t *testing.T) {
	t.Parallel()

	limited := `2026-09-25T10:00:00.000000000Z {"level":"error","logger":"tls.obtain","msg":"could not get certificate from issuer","identifier":"shop.example.com","issuer":"acme-v02.api.letsencrypt.org-directory","error":"HTTP 429 urn:ietf:params:acme:error:rateLimited - too many certificates (50) already issued for \"example.com\" in the last 168h0m0s, retry after ` + time.Now().Add(24*time.Hour).UTC().Format("2006-01-02 15:04:05 MST") + `"}`
	machine := &box{logs: limited}
	certificate, err := (caddy.Builtin{Box: machine}).Certificate(context.Background(), "shop.example.com")
	if err != nil {
		t.Fatalf("Certificate() = %v", err)
	}
	if certificate.Trouble == nil {
		t.Error("Certificate() found no trouble in a log that says the CA refused the name for its rate limit")
	}
	if certificate.Renewal == "" {
		t.Error("Certificate() says nothing about who renews what the proxy orders")
	}
	if len(machine.ran) != 1 {
		t.Errorf("Certificate() ran %q, want the one read of what the proxy logged", machine.ran)
	}

	quiet, err := (caddy.Builtin{Box: &box{}}).Certificate(context.Background(), "shop.example.com")
	if err != nil || quiet.Trouble != nil {
		t.Errorf("Certificate() over a proxy that logged nothing = %+v, %v, want no trouble", quiet, err)
	}
	engineless := errors.New("Cannot connect to the Docker daemon")
	if _, err := (caddy.Builtin{Box: &box{unreached: engineless}}).Certificate(context.Background(), "shop.example.com"); !errors.Is(err, engineless) {
		t.Errorf("Certificate() over an engine it could not reach = %v, want that returned rather than read as a proxy with nothing to say", err)
	}
}
