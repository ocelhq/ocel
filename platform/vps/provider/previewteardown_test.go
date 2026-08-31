package vps_test

import (
	"context"
	"strings"
	"testing"

	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func overABox(t *testing.T, machine *box) *vps.Provider {
	t.Helper()

	return vps.ProviderOver(
		vps.Options{SSH: vps.Target{Host: "box.invalid", User: "ada"}},
		func(context.Context) (host.Conn, error) { return machine, nil },
	)
}

func forgetting(t *testing.T, machine *box) []string {
	t.Helper()

	var driven []string
	for _, command := range machine.commands() {
		if strings.Contains(command, "'forget'") {
			driven = append(driven, command)
		}
	}
	return driven
}

func TestForgettingAPreviewsCertificatesDrivesTheHelperOnceForEveryHostnameItClaimed(t *testing.T) {
	t.Parallel()

	machine := &box{}
	hostnames := []string{"shop--pr-7--api.preview.example.com", "shop--pr-7--web.preview.example.com"}

	if err := overABox(t, machine).Host().ForgetCertificates(context.Background(), hostnames, nil); err != nil {
		t.Fatalf("ForgetCertificates = %v", err)
	}

	driven := forgetting(t, machine)
	if len(driven) != 1 {
		t.Fatalf("the helper was driven %d times for %d hostnames: %v", len(driven), len(hostnames), driven)
	}
	if !strings.HasPrefix(driven[0], "'docker' 'exec' '"+host.ProxyContainer+"' '"+host.ProxyHelperMount+"'") {
		t.Errorf("the certificates were reached with %q, want the helper inside the proxy: the store is bound into that container alone and this session deploys as a login that cannot read it from the host", driven[0])
	}
	for _, hostname := range hostnames {
		if !strings.Contains(driven[0], "'"+hostname+"'") {
			t.Errorf("%q names no %s", driven[0], hostname)
		}
	}
}

func TestForgettingNoHostnamesReachesTheBoxNotAtAll(t *testing.T) {
	t.Parallel()

	machine := &box{}
	if err := overABox(t, machine).Host().ForgetCertificates(context.Background(), nil, nil); err != nil {
		t.Fatalf("ForgetCertificates(nothing) = %v", err)
	}
	if driven := forgetting(t, machine); len(driven) != 0 {
		t.Errorf("a preview claiming no hostname still drove %v", driven)
	}
}

func TestForgettingReportsEachPairItTookOffTheBox(t *testing.T) {
	t.Parallel()

	removed := "/data/caddy/certificates/acme-v02.api.letsencrypt.org-directory/shop--pr-7--web.preview.example.com"
	machine := &box{refuses: func(command string) (session.Result, bool) {
		if !strings.Contains(command, "'forget'") {
			return session.Result{}, false
		}
		return session.Result{Stdout: removed + "\n"}, true
	}}
	spoken := &said{}

	if err := overABox(t, machine).Host().ForgetCertificates(context.Background(),
		[]string{"shop--pr-7--web.preview.example.com"}, spoken); err != nil {
		t.Fatalf("ForgetCertificates = %v", err)
	}
	if spoken.at(removed) < 0 {
		t.Errorf("the teardown said %v, want the pair it took named: bytes left behind after a teardown are the term that keeps growing, so what was reclaimed is what the run has to show", spoken.lines)
	}
}

func TestAHelperThatRefusesTheStoreIsSurfacedRatherThanCountedAsForgotten(t *testing.T) {
	t.Parallel()

	machine := &box{refuses: func(command string) (session.Result, bool) {
		if !strings.Contains(command, "'forget'") {
			return session.Result{}, false
		}
		return session.Result{Code: 2, Stderr: "ocel-proxyctl: open /data/caddy/certificates: permission denied"}, true
	}}
	spoken := &said{}

	err := overABox(t, machine).Host().ForgetCertificates(context.Background(),
		[]string{"shop--pr-7--web.preview.example.com"}, spoken)
	if err == nil {
		t.Fatal("a helper that could not read the store reported the pairs forgotten, and the teardown then goes on to empty the ledger that names which hostnames are still to reach")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("the refusal reads %q, and what the helper said is the whole of why this box kept the pairs", err)
	}
	if len(spoken.lines) != 0 {
		t.Errorf("a refused forget still reported %v as taken off the box", spoken.lines)
	}
}
