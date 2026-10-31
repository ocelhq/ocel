package vps

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	gossh "golang.org/x/crypto/ssh"
	"google.golang.org/protobuf/types/known/timestamppb"

	endpointv1 "github.com/ocelhq/ocel/pkg/proto/endpoint/v1"
	"github.com/ocelhq/ocel/pkg/proto/endpoint/v1/endpointv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const boxHostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"

type boxAuthority struct {
	signer gossh.Signer
	refuse bool

	mu      sync.Mutex
	bearers []string
}

func newBoxAuthority(t *testing.T) *boxAuthority {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return &boxAuthority{signer: signer}
}

func (b *boxAuthority) IssueUserCertificate(ctx context.Context, request *endpointv1.IssueUserCertificateRequest) (*endpointv1.IssueUserCertificateResponse, error) {
	b.mu.Lock()
	b.bearers = append(b.bearers, connectHeader(ctx))
	b.mu.Unlock()
	if b.refuse {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("this box trusts the console for no preview deploys"))
	}
	key, _, _, _, err := gossh.ParseAuthorizedKey([]byte(request.GetPublicKey()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	expires := time.Now().Add(10 * time.Minute)
	certificate := &gossh.Certificate{Key: key, CertType: gossh.UserCert, ValidPrincipals: []string{"ocel-deploy"}, ValidBefore: uint64(expires.Unix())}
	if err := certificate.SignCert(rand.Reader, b.signer); err != nil {
		return nil, err
	}
	return &endpointv1.IssueUserCertificateResponse{
		Certificate: strings.TrimSpace(string(gossh.MarshalAuthorizedKey(certificate))),
		Principal:   "ocel-deploy",
		ExpiresAt:   timestamppb.New(expires),
		HostKeys:    []string{boxHostKey},
	}, nil
}

type headerKey struct{}

func connectHeader(ctx context.Context) string {
	said, _ := ctx.Value(headerKey{}).(string)
	return said
}

func servedAuthority(t *testing.T, authority *boxAuthority) *httptest.Server {
	t.Helper()
	path, handler := endpointv1connect.NewSshCertificateServiceHandler(authority)
	mux := http.NewServeMux()
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), headerKey{}, r.Header.Get("Authorization"))))
	}))
	served := httptest.NewTLSServer(mux)
	t.Cleanup(served.Close)
	return served
}

func loginAt(t *testing.T, served *httptest.Server, token string) *certificateLogin {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	login := &certificateLogin{
		identity: provider.WebIdentity{TokenFile: tokenFile, IdentityProvider: served.URL},
		host:     "box.example.com",
		client:   served.Client(),
		now:      time.Now,
	}
	t.Cleanup(func() { _ = login.close() })
	return login
}

func TestADeployWithACredentialLogsInWithACertificateForAFreshKey(t *testing.T) {
	authority := newBoxAuthority(t)
	login := loginAt(t, servedAuthority(t, authority), "credential-token")

	target, err := login.target(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if target.Host != "box.example.com" || target.User != "ocel-deploy" || target.Config != ssh.NoConfig {
		t.Errorf("target = %+v, want box.example.com as ocel-deploy reading no ssh config", target)
	}
	if got := authority.bearers; len(got) != 1 || got[0] != "Bearer credential-token" {
		t.Errorf("the box was asked with %q, want the console's credential as the bearer", got)
	}

	info, err := os.Stat(target.IdentityFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key %s: %v, mode %v, want a key only this run reads", target.IdentityFile, err, info)
	}
	rawKey, err := os.ReadFile(target.IdentityFile)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.ParsePrivateKey(rawKey)
	if err != nil {
		t.Fatal(err)
	}
	rawCertificate, err := os.ReadFile(target.CertificateFile)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, _, _, err := gossh.ParseAuthorizedKey(rawCertificate)
	if err != nil {
		t.Fatal(err)
	}
	certificate, ok := parsed.(*gossh.Certificate)
	if !ok || string(certificate.Key.Marshal()) != string(signer.PublicKey().Marshal()) {
		t.Error("the certificate written certifies another key than the one written beside it")
	}
	pinned, err := os.ReadFile(target.KnownHostsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(pinned), "box.example.com "+boxHostKey+"\n"; got != want {
		t.Errorf("known hosts = %q, want only the host keys the box named, %q", got, want)
	}
}

func TestAKnownHostOnAnUnusualPortIsPinnedUnderThatPort(t *testing.T) {
	login := loginAt(t, servedAuthority(t, newBoxAuthority(t)), "credential-token")
	login.port = 2222
	target, err := login.target(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := os.ReadFile(target.KnownHostsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(pinned), "[box.example.com]:2222 ") || target.Port != 2222 {
		t.Errorf("known hosts = %q, port %d, want the bracketed entry ssh looks up on port 2222", pinned, target.Port)
	}
}

func TestABoxThatRefusesTheCredentialRefusesTheDeploy(t *testing.T) {
	authority := newBoxAuthority(t)
	authority.refuse = true
	_, err := loginAt(t, servedAuthority(t, authority), "credential-token").target(context.Background())
	if !refusedWith(err, refusal.CodeDenied) || !strings.Contains(err.Error(), "no preview deploys") {
		t.Fatalf("err = %v, want the box's own refusal", err)
	}
}

func TestACertificateIsRenewedHalfwayToItsEnd(t *testing.T) {
	if delay := renewalDelay(time.Unix(0, 0), time.Unix(600, 0)); delay != 5*time.Minute {
		t.Errorf("renewal delay = %s, want halfway to the end", delay)
	}
	if delay := renewalDelay(time.Unix(0, 0), time.Unix(2, 0)); delay != shortestRenewal {
		t.Errorf("renewal delay = %s, want the floor %s", delay, shortestRenewal)
	}
}

func TestClosingTheLoginRemovesItsKey(t *testing.T) {
	login := loginAt(t, servedAuthority(t, newBoxAuthority(t)), "credential-token")
	target, err := login.target(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := login.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target.IdentityFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stat %s = %v, want the key gone", target.IdentityFile, err)
	}
}

func TestAWebIdentityIsTheHTTPSURLOfABoxNamedByItsHostname(t *testing.T) {
	for name, url := range map[string]string{
		"an address": "https://203.0.113.10" + boxapi.ConnectorPath,
		"plain http": "http://box.example.com" + boxapi.ConnectorPath,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newCertificateLogin(provider.WebIdentity{TokenFile: "/run/token", IdentityProvider: url}, "box.example.com", 0); err == nil {
				t.Fatalf("newCertificateLogin(%q) accepted it", url)
			}
		})
	}
}

func TestTheVPSProviderDeploysWithAWebIdentity(t *testing.T) {
	built, err := New(TestAssets)(context.Background(), provider.Settings{
		Options:     provider.Options{"ssh": "box.example.com"},
		WebIdentity: &provider.WebIdentity{TokenFile: "/run/token", IdentityProvider: "https://box.example.com" + boxapi.ConnectorPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	if built.Facts().WebIdentity == nil {
		t.Error("Facts().WebIdentity = nil, want the provider to deploy with a console credential")
	}
}

func TestACredentialForAnotherBoxThanTheProjectNamesIsRefused(t *testing.T) {
	_, err := New(TestAssets)(context.Background(), provider.Settings{
		Options:     provider.Options{"ssh": map[string]any{"host": "app.example.com", "user": "deploy"}},
		WebIdentity: &provider.WebIdentity{TokenFile: "/run/token", IdentityProvider: "https://build.example.com" + boxapi.ConnectorPath},
	})
	if !refusedWith(err, refusal.CodeInvalid) || !strings.Contains(err.Error(), "app.example.com") || !strings.Contains(err.Error(), "build.example.com") {
		t.Fatalf("err = %v, want both boxes named", err)
	}
}

func TestACertificateLoginDialsTheBoxTheProjectNames(t *testing.T) {
	login, err := newCertificateLogin(provider.WebIdentity{TokenFile: "/run/token", IdentityProvider: "https://Box.Example.com" + boxapi.ConnectorPath}, "box.example.com", 2222)
	if err != nil {
		t.Fatal(err)
	}
	if login.host != "box.example.com" || login.port != 2222 {
		t.Errorf("login dials %s port %d, want the project's box and port", login.host, login.port)
	}
}
