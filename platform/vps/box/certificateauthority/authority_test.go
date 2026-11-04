package certificateauthority

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/crypto/ssh"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"
	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	endpointv1 "github.com/ocelhq/ocel/pkg/proto/endpoint/v1"
	"github.com/ocelhq/ocel/platform/vps/box/usercertificate"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

var minted = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

const hostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"

func authorityKey(t *testing.T) []byte {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "ocel user ca")
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block)
}

func newAuthority(t *testing.T, tiers ...environment.Tier) *Authority {
	t.Helper()
	authority, _ := signingAuthority(t, tiers...)
	return authority
}

func signingAuthority(t *testing.T, tiers ...environment.Tier) (*Authority, ssh.PublicKey) {
	t.Helper()
	if len(tiers) == 0 {
		tiers = []environment.Tier{environment.TierProduction}
	}
	key := authorityKey(t)
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := New(Config{
		Principal: "ocel-deploy",
		Sign: func(_ context.Context, request boxapi.CertificateRequest) (string, error) {
			return usercertificate.Sign(key, "ocel-deploy", request, minted)
		},
		HostKeys: func() ([]string, error) { return []string{hostKey}, nil },
		Tiers:    tiers,
		Now:      func() time.Time { return minted },
	})
	if err != nil {
		t.Fatal(err)
	}
	return authority, signer.PublicKey()
}

func requestKey(t *testing.T) (string, ssh.PublicKey) {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), key
}

func credentialCaller(tier environment.Tier, expires time.Time) endpoint.Caller {
	return endpoint.NewCredentialCaller("https://console.example.com", "org-1", endpoint.TargetCredential{
		TargetID:  "target-1",
		ProjectID: "project-1",
		Purpose:   "deploy",
		TokenID:   "4f1c2a3b-5d6e-4f70-8a9b-0c1d2e3f4a5b",
		Tier:      tier,
		Expires:   expires,
	})
}

func issue(t *testing.T, authority *Authority, caller endpoint.Caller) (*endpointv1.IssueUserCertificateResponse, ssh.PublicKey, error) {
	t.Helper()
	line, key := requestKey(t)
	issued, err := authority.IssueUserCertificate(endpoint.WithCaller(context.Background(), caller), &endpointv1.IssueUserCertificateRequest{PublicKey: line})
	return issued, key, err
}

func parsedCertificate(t *testing.T, issued *endpointv1.IssueUserCertificateResponse) *ssh.Certificate {
	t.Helper()
	parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(issued.GetCertificate()))
	if err != nil {
		t.Fatalf("certificate %q: %v", issued.GetCertificate(), err)
	}
	certificate, ok := parsed.(*ssh.Certificate)
	if !ok {
		t.Fatalf("%q is a %T, not a certificate", issued.GetCertificate(), parsed)
	}
	return certificate
}

func TestACertificateIsForTheRequestedKeyAndTheBoxsPrincipalAlone(t *testing.T) {
	authority, public := signingAuthority(t)
	issued, key, err := issue(t, authority, credentialCaller(environment.TierProduction, minted.Add(10*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	certificate := parsedCertificate(t, issued)

	if certificate.CertType != ssh.UserCert {
		t.Errorf("certificate type = %d, want a user certificate", certificate.CertType)
	}
	if string(certificate.Key.Marshal()) != string(key.Marshal()) {
		t.Error("the certificate certifies another key than the one asked for")
	}
	if got := certificate.ValidPrincipals; len(got) != 1 || got[0] != "ocel-deploy" {
		t.Errorf("principals = %q, want only ocel-deploy", got)
	}
	if issued.GetPrincipal() != "ocel-deploy" {
		t.Errorf("principal = %q, want ocel-deploy, the one the box's config names", issued.GetPrincipal())
	}
	checker := ssh.CertChecker{
		IsUserAuthority: func(auth ssh.PublicKey) bool {
			return string(auth.Marshal()) == string(public.Marshal())
		},
		Clock: func() time.Time { return minted },
	}
	if err := checker.CheckCert("ocel-deploy", certificate); err != nil {
		t.Errorf("the box's own authority does not accept the certificate for ocel-deploy: %v", err)
	}
	if err := checker.CheckCert("root", certificate); err == nil {
		t.Error("the certificate is accepted for root")
	}
}

func TestACertificateNamesTheCredentialsSessionProjectAndTierAsItsKeyID(t *testing.T) {
	issued, _, err := issue(t, newAuthority(t), credentialCaller(environment.TierProduction, minted.Add(10*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := parsedCertificate(t, issued).KeyId, "ocel-4f1c2a3b-5d6 project-1 production"; got != want {
		t.Errorf("key id = %q, want %q", got, want)
	}
}

func TestACertificateLivesTenMinutesAtMost(t *testing.T) {
	issued, _, err := issue(t, newAuthority(t), credentialCaller(environment.TierProduction, minted.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	certificate := parsedCertificate(t, issued)
	after := time.Unix(int64(certificate.ValidAfter), 0)
	before := time.Unix(int64(certificate.ValidBefore), 0)
	if lifetime := before.Sub(after); lifetime > 10*time.Minute {
		t.Errorf("the certificate is valid for %s, more than ten minutes", lifetime)
	}
	if after.After(minted) || !before.After(minted) {
		t.Errorf("the certificate is valid from %s to %s, which does not cover now (%s)", after, before, minted)
	}
	if !issued.GetExpiresAt().AsTime().Equal(before) {
		t.Errorf("expires_at = %s, want the certificate's own end %s", issued.GetExpiresAt().AsTime(), before)
	}
}

func TestACertificateEndsNoLaterThanTheCredentialItWasIssuedFor(t *testing.T) {
	expires := minted.Add(3 * time.Minute)
	issued, _, err := issue(t, newAuthority(t), credentialCaller(environment.TierProduction, expires))
	if err != nil {
		t.Fatal(err)
	}
	if before := time.Unix(int64(parsedCertificate(t, issued).ValidBefore), 0); before.After(expires) {
		t.Errorf("the certificate is valid until %s, after the credential's expiry %s", before, expires)
	}
}

func TestTheBoxsHostKeysComeBackWithTheCertificate(t *testing.T) {
	issued, _, err := issue(t, newAuthority(t), credentialCaller(environment.TierProduction, minted.Add(10*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	if got := issued.GetHostKeys(); len(got) != 1 || got[0] != hostKey {
		t.Errorf("host keys = %q, want [%q]", got, hostKey)
	}
}

func TestACredentialThatHasExpiredGetsNoCertificate(t *testing.T) {
	_, _, err := issue(t, newAuthority(t), credentialCaller(environment.TierProduction, minted.Add(-time.Second)))
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("err = %v, want Unauthenticated", err)
	}
}

func TestACredentialForATierThisBoxDoesNotTrustGetsNoCertificate(t *testing.T) {
	_, _, err := issue(t, newAuthority(t, environment.TierProduction), credentialCaller(environment.TierPreview, minted.Add(10*time.Minute)))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("err = %v, want PermissionDenied", err)
	}
	if !strings.Contains(err.Error(), "ocel bootstrap preview") {
		t.Errorf("err = %v, want it to name the bootstrap that grants the tier", err)
	}
}

func TestACredentialForAnotherPurposeGetsNoCertificate(t *testing.T) {
	credential, _ := credentialCaller(environment.TierProduction, minted.Add(10*time.Minute)).Credential()
	credential.Purpose = "bootstrap"
	caller := endpoint.NewCredentialCaller("https://console.example.com", "org-1", credential)
	_, _, err := issue(t, newAuthority(t), caller)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("err = %v, want PermissionDenied", err)
	}
}

func TestOnlyATargetCredentialGetsACertificate(t *testing.T) {
	person, err := endpoint.NewUserCaller("https://console.example.com", "org-1", "user-1", scopev1.Role_ROLE_MEMBER, []scopev1.Scope{scopev1.Scope_SCOPE_APPS_WRITE})
	if err != nil {
		t.Fatal(err)
	}
	for name, caller := range map[string]endpoint.Caller{
		"a person": person,
		"a job":    endpoint.NewJobCaller("https://console.example.com", "org-1", endpoint.Job{ID: "job-1", Tier: environment.TierProduction}),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := issue(t, newAuthority(t), caller)
			if connect.CodeOf(err) != connect.CodePermissionDenied {
				t.Fatalf("err = %v, want PermissionDenied", err)
			}
		})
	}
}

func TestAnAuthorityNeedsAPrincipalAndASigner(t *testing.T) {
	sign := func(context.Context, boxapi.CertificateRequest) (string, error) { return "", nil }
	for name, config := range map[string]Config{
		"no signer":    {Principal: "ocel-deploy"},
		"no principal": {Sign: sign},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(config); err == nil {
				t.Fatal("New accepted it")
			}
		})
	}
}

func TestASignerThatRefusesLeavesTheCallerWithoutACertificate(t *testing.T) {
	authority, err := New(Config{
		Principal: "ocel-deploy",
		Sign: func(context.Context, boxapi.CertificateRequest) (string, error) {
			return "", errors.New("ocel-live ssh-sign: the certificate asked for lasts 1h0m0s")
		},
		Tiers: []environment.Tier{environment.TierProduction},
		Now:   func() time.Time { return minted },
	})
	if err != nil {
		t.Fatal(err)
	}
	issued, _, err := issue(t, authority, credentialCaller(environment.TierProduction, minted.Add(10*time.Minute)))
	if err == nil || issued != nil {
		t.Fatalf("issued %v, %v, want the signer's refusal", issued, err)
	}
}
