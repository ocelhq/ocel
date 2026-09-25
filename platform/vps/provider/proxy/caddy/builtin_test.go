package caddy_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

type box struct {
	ran      []string
	answer   func(command string) (string, error)
	logs     string
	served   []byte
	unserved error
}

func (b *box) Ran(_ context.Context, _, command string) (string, error) {
	b.ran = append(b.ran, command)
	if b.answer == nil {
		return "", nil
	}
	return b.answer(command)
}

func (b *box) Said(_ context.Context, command string) string {
	b.ran = append(b.ran, command)
	return b.logs
}

func (b *box) Loopback(context.Context, string) ([]byte, error) { return b.served, b.unserved }

func TestAdmittingReloadsTheRunningProxyOntoItsConfigOverTheAdminSocket(t *testing.T) {
	t.Parallel()

	held := &box{}
	if err := caddy.New(held).Admit(context.Background(), admitting(proxy.Entry{Hostname: "shop.example.com"})); err != nil {
		t.Fatalf("Admit() = %v", err)
	}
	if len(held.ran) != 1 {
		t.Fatalf("Admit() ran %q, want the one reload", held.ran)
	}
	for _, wanted := range []string{"'docker' 'exec' '" + caddy.Container + "' 'caddy' 'reload'", "'--config' '" + caddy.ConfigMount + "'", "'--address' 'unix/" + caddy.AdminSocket + "'"} {
		if !strings.Contains(held.ran[0], wanted) {
			t.Errorf("Admit() ran %q, which carries no %s", held.ran[0], wanted)
		}
	}
	if strings.Contains(held.ran[0], "--force") {
		t.Errorf("Admit() ran %q: a forced reload restarts every server caddy runs even when nothing changed", held.ran[0])
	}
}

func TestAnAdmissionTheProxyCouldNotServeReloadsNothing(t *testing.T) {
	t.Parallel()

	held := &box{}
	if err := caddy.New(held).Admit(context.Background(), proxy.Admission{Entries: []proxy.Entry{{Hostname: "*.example.com"}}}); err == nil {
		t.Error("Admit() of a wildcard with no upstream = nil")
	}
	if len(held.ran) != 0 {
		t.Errorf("Admit() of an admission it refuses ran %q", held.ran)
	}
}

func TestAReloadTheProxyRefusesIsTheAdmissionsFailure(t *testing.T) {
	t.Parallel()

	refused := errors.New("adapting config using json: loading tls app: boom")
	held := &box{answer: func(string) (string, error) { return "", refused }}
	if err := caddy.New(held).Admit(context.Background(), admitting(proxy.Entry{Hostname: "shop.example.com"})); !errors.Is(err, refused) {
		t.Errorf("Admit() = %v, want the refused reload carried out", err)
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

func TestTheStandingReadsWhetherTheAdminApiListensOnAPortInsideTheProxy(t *testing.T) {
	t.Parallel()

	for what, check := range map[string]struct {
		said    string
		err     error
		verdict providerkit.StandingVerdict
	}{
		"only the serving ports":          {said: listened("0050", "01BB"), verdict: providerkit.StandingPass},
		"the admin port beside them":      {said: listened("0050", "01BB", "07E3"), verdict: providerkit.StandingFail},
		"nothing at all":                  {said: tcpHeader, verdict: providerkit.StandingFail},
		"a proxy that answered nothing":   {err: errors.New("no such container"), verdict: providerkit.StandingFail},
		"a table that is no socket table": {said: "garbage\n", verdict: providerkit.StandingFail},
	} {
		held := &box{answer: func(string) (string, error) { return check.said, check.err }}
		standing, err := caddy.New(held).Inspect(context.Background())
		if err != nil {
			t.Fatalf("Inspect() over %s = %v", what, err)
		}
		if len(standing) != 1 || standing[0].Verdict != check.verdict {
			t.Errorf("Inspect() over %s = %+v, want one %v verdict", what, standing, check.verdict)
		}
		if len(held.ran) != 1 || !strings.Contains(held.ran[0], "'docker' 'exec' '"+caddy.Container+"'") || !strings.Contains(held.ran[0], "/proc/net/tcp") {
			t.Errorf("Inspect() over %s ran %q, want the proxy's own socket tables read from inside it", what, held.ran)
		}
	}
}

func leafFor(t *testing.T, hostname string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})
}

func TestTheCertificateIsWhatTheBoxServesAndTheTroubleIsWhatTheProxyLogged(t *testing.T) {
	t.Parallel()

	limited := `2026-09-25T10:00:00.000000000Z {"level":"error","logger":"tls.obtain","msg":"could not get certificate from issuer","identifier":"shop.example.com","issuer":"acme-v02.api.letsencrypt.org-directory","error":"HTTP 429 urn:ietf:params:acme:error:rateLimited - too many certificates (50) already issued for \"example.com\" in the last 168h0m0s, retry after ` + time.Now().Add(24*time.Hour).UTC().Format("2006-01-02 15:04:05 MST") + `"}`
	held := &box{logs: limited, served: leafFor(t, "shop.example.com")}
	certificate, err := caddy.New(held).Certificate(context.Background(), "shop.example.com")
	if err != nil {
		t.Fatalf("Certificate() = %v", err)
	}
	if certificate.Served == nil || !certificate.Served.Covers("shop.example.com") {
		t.Errorf("Certificate() served %+v, want the leaf the box presents for the name", certificate.Served)
	}
	if certificate.Trouble == nil {
		t.Error("Certificate() found no trouble in a log that says the CA refused the name for its rate limit")
	}
	if certificate.Renewal == "" {
		t.Error("Certificate() says nothing about who renews what the proxy orders")
	}

	pending, err := caddy.New(&box{}).Certificate(context.Background(), "shop.example.com")
	if err != nil || pending.Served != nil || pending.Trouble != nil {
		t.Errorf("Certificate() over a box serving nothing yet = %+v, %v, want nothing served and no trouble", pending, err)
	}
	unreached := errors.New("the loopback read failed")
	if _, err := caddy.New(&box{unserved: unreached}).Certificate(context.Background(), "shop.example.com"); !errors.Is(err, unreached) {
		t.Errorf("Certificate() over a loopback that failed = %v, want the failure carried out", err)
	}
}

func TestForgettingRefusesANameThatIsAPathRatherThanAHostnameAndRunsNothing(t *testing.T) {
	t.Parallel()

	for _, named := range []string{"../../caddy", "shop.example.com/..", "", "*.preview.example.com", "wildcard_.preview.example.com", "a b.example.com"} {
		held := &box{}
		if _, err := caddy.New(held).Forget(context.Background(), []string{"fine.example.com", named}); err == nil {
			t.Errorf("Forget(%q) = nil, and the name reaches rm -rf under the proxy's data", named)
		}
		if len(held.ran) != 0 {
			t.Errorf("Forget(%q) ran %q", named, held.ran)
		}
	}
	held := &box{}
	if removed, err := caddy.New(held).Forget(context.Background(), nil); err != nil || removed != nil || len(held.ran) != 0 {
		t.Errorf("Forget(nothing) = %v, %v and ran %q, want a no-op", removed, err, held.ran)
	}
}

func stored(t *testing.T, root, issuer, subject string) string {
	t.Helper()
	held := filepath.Join(root, issuer, subject)
	if err := os.MkdirAll(held, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{subject + ".crt", subject + ".key", subject + ".json"} {
		if err := os.WriteFile(filepath.Join(held, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return held
}

type shell struct {
	box
	path string
}

func (s *shell) Ran(_ context.Context, _, command string) (string, error) {
	run := exec.Command("/bin/sh", "-c", command)
	run.Env = []string{"PATH=" + s.path}
	said, err := run.Output()
	return string(said), err
}

func TestForgettingTakesEveryPairIssuedForTheNamesFromEveryIssuerAndNothingElse(t *testing.T) {
	root := t.TempDir()
	defer caddy.StoreCertificatesAt(root)()
	stub := t.TempDir()
	if err := os.WriteFile(filepath.Join(stub, "docker"), []byte("#!/bin/sh\nshift 2\nexec \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"sh", "rm", "printf", "cat"} {
		if found, err := exec.LookPath(tool); err == nil {
			_ = os.Symlink(found, filepath.Join(stub, tool))
		}
	}
	live := "acme-v02.api.letsencrypt.org-directory"
	staging := "acme-staging-v02.api.letsencrypt.org-directory"
	going := []string{
		stored(t, root, live, "shop--pr-7--web.preview.example.com"),
		stored(t, root, staging, "shop--pr-7--web.preview.example.com"),
		stored(t, root, live, "shop--pr-7--api.preview.example.com"),
	}
	staying := stored(t, root, live, "shop--pr-9--web.preview.example.com")
	wildcard := stored(t, root, live, "wildcard_.preview.example.com")
	if err := os.WriteFile(filepath.Join(root, "acme.key"), []byte("account"), 0o600); err != nil {
		t.Fatal(err)
	}

	removed, err := caddy.New(&shell{path: stub}).Forget(context.Background(), []string{"shop--pr-7--web.preview.example.com", "shop--pr-7--api.preview.example.com", "never.example.com"})
	if err != nil {
		t.Fatalf("Forget() = %v", err)
	}
	slices.Sort(removed)
	slices.Sort(going)
	if !slices.Equal(removed, going) {
		t.Errorf("Forget() reported %q, want exactly %q", removed, going)
	}
	for _, path := range going {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still stands after it was forgotten", path)
		}
	}
	for _, path := range []string{staying, wildcard, filepath.Join(root, "acme.key")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was taken with the names forgotten: %v", path, err)
		}
	}
}
