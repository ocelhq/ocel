package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ocelhq/ocel/platform/vps/box/usercertificate"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

func authorityIn(t *testing.T) authorityFiles {
	t.Helper()
	dir := t.TempDir()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		t.Fatal(err)
	}
	files := authorityFiles{key: filepath.Join(dir, "user-ca"), config: filepath.Join(dir, "authority.json")}
	if err := os.WriteFile(files.key, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files.config, []byte(`{"principal":"ocel-deploy"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return files
}

func signRequest(t *testing.T, principal string, now time.Time, lifetime time.Duration) []byte {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(boxapi.CertificateRequest{
		PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), Principal: principal,
		KeyID: "ocel-4f1c2a3b-5d6 project-1 preview", ValidAfter: now.Unix(), ValidBefore: now.Add(lifetime).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestTheSignCommandPrintsACertificateForTheConfiguredPrincipal(t *testing.T) {
	now := time.Now()
	var stdout, stderr bytes.Buffer
	code := signCertificate(authorityIn(t), nil, bytes.NewReader(signRequest(t, "ocel-deploy", now, usercertificate.Lifetime)), &stdout, &stderr, now)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "ssh-ed25519-cert-v01@openssh.com ") {
		t.Errorf("stdout = %q, want one certificate", stdout.String())
	}
}

func TestTheSignCommandRefusesAnotherPrincipalALongerValidityAndArguments(t *testing.T) {
	now := time.Now()
	for name, run := range map[string]func(files authorityFiles, stdout, stderr *bytes.Buffer) int{
		"another principal": func(files authorityFiles, stdout, stderr *bytes.Buffer) int {
			return signCertificate(files, nil, bytes.NewReader(signRequest(t, "root", now, time.Minute)), stdout, stderr, now)
		},
		"a longer validity": func(files authorityFiles, stdout, stderr *bytes.Buffer) int {
			return signCertificate(files, nil, bytes.NewReader(signRequest(t, "ocel-deploy", now, time.Hour)), stdout, stderr, now)
		},
		"arguments": func(files authorityFiles, stdout, stderr *bytes.Buffer) int {
			return signCertificate(files, []string{"--principal", "root"}, bytes.NewReader(signRequest(t, "ocel-deploy", now, time.Minute)), stdout, stderr, now)
		},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(authorityIn(t), &stdout, &stderr); code == 0 || stdout.Len() != 0 {
				t.Fatalf("exit %d, stdout %q, want a refusal and no certificate", code, stdout.String())
			}
		})
	}
}
