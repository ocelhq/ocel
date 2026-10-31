package usercertificate

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

var now = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func authority(t *testing.T) ([]byte, ssh.PublicKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(block), key
}

func asked(t *testing.T) boxapi.CertificateRequest {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return boxapi.CertificateRequest{
		PublicKey:   strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))),
		Principal:   "ocel-deploy",
		KeyID:       "ocel-4f1c2a3b-5d6 project-1 production",
		ValidAfter:  now.Add(-Backdate).Unix(),
		ValidBefore: now.Add(-Backdate + Lifetime).Unix(),
	}
}

func TestASignedCertificateLetsInTheConfiguredPrincipalAlone(t *testing.T) {
	key, public := authority(t)
	signed, err := Sign(key, "ocel-deploy", asked(t), now)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(signed))
	if err != nil {
		t.Fatal(err)
	}
	certificate := parsed.(*ssh.Certificate)
	checker := ssh.CertChecker{
		IsUserAuthority: func(auth ssh.PublicKey) bool { return string(auth.Marshal()) == string(public.Marshal()) },
		Clock:           func() time.Time { return now },
	}
	if err := checker.CheckCert("ocel-deploy", certificate); err != nil {
		t.Errorf("the certificate is refused for ocel-deploy: %v", err)
	}
	if err := checker.CheckCert("root", certificate); err == nil {
		t.Error("the certificate lets in root")
	}
	if certificate.KeyId != "ocel-4f1c2a3b-5d6 project-1 production" {
		t.Errorf("key id = %q", certificate.KeyId)
	}
}

func TestTheSignerRefusesWhatTheConnectorMayNotAskFor(t *testing.T) {
	key, _ := authority(t)
	for name, change := range map[string]func(*boxapi.CertificateRequest){
		"another principal": func(r *boxapi.CertificateRequest) { r.Principal = "root" },
		"a longer validity": func(r *boxapi.CertificateRequest) {
			r.ValidBefore = r.ValidAfter + int64((Lifetime + time.Second).Seconds())
		},
		"a start long past": func(r *boxapi.CertificateRequest) {
			r.ValidAfter, r.ValidBefore = now.Add(-time.Hour).Unix(), now.Add(-time.Hour+Lifetime).Unix()
		},
		"an end far ahead": func(r *boxapi.CertificateRequest) {
			r.ValidAfter, r.ValidBefore = now.Add(time.Hour).Unix(), now.Add(time.Hour+Lifetime).Unix()
		},
		"an end before its start": func(r *boxapi.CertificateRequest) { r.ValidBefore = r.ValidAfter },
		"a free-form key id":      func(r *boxapi.CertificateRequest) { r.KeyID = "anything at all" },
		"no key":                  func(r *boxapi.CertificateRequest) { r.PublicKey = "" },
	} {
		t.Run(name, func(t *testing.T) {
			request := asked(t)
			change(&request)
			if _, err := Sign(key, "ocel-deploy", request, now); err == nil {
				t.Fatal("Sign signed it")
			}
		})
	}
}
