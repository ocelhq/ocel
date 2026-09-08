package gcp_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

func withoutAnAmbientProject(t *testing.T) {
	t.Helper()
	withoutApplicationDefaultCredentials(t)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("CLOUDSDK_CORE_PROJECT", "")
	t.Setenv("PATH", t.TempDir())
}

func targeted(t *testing.T, options providerkit.Options) string {
	t.Helper()
	p, err := gcp.New(context.Background(), options)
	if err != nil {
		t.Fatalf("New(%v) = %v, want a provider", options, err)
	}
	credentials, named := p.Credentials().(gcp.Credentials)
	if !named {
		t.Fatalf("Credentials() = %T, want this provider's own", p.Credentials())
	}
	return credentials.Project
}

func TestAProjectNamedInTheOptionsIsTheProjectTheRunTargets(t *testing.T) {
	withoutAnAmbientProject(t)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "ambient-elsewhere")

	if got := targeted(t, providerkit.Options{"project": "acme-prod", "region": "europe-west1"}); got != "acme-prod" {
		t.Errorf("the run targets %q, want the project the options named", got)
	}
}

func TestAProjectNobodyNamedIsTakenFromTheEnvironmentTheRunCarries(t *testing.T) {
	withoutAnAmbientProject(t)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "ambient-prod")

	if got := targeted(t, providerkit.Options{"region": "europe-west1"}); got != "ambient-prod" {
		t.Errorf("the run targets %q, want the ambient project: switching credentials is how one project deploys production and another previews", got)
	}
}

func TestTheCloudSDKProjectIsReadWhenNothingBeforeItNamesOne(t *testing.T) {
	withoutAnAmbientProject(t)
	t.Setenv("CLOUDSDK_CORE_PROJECT", "sdk-prod")

	if got := targeted(t, providerkit.Options{"region": "europe-west1"}); got != "sdk-prod" {
		t.Errorf("the run targets %q, want the project CLOUDSDK_CORE_PROJECT names", got)
	}
}

func TestTheAmbientProjectIsReadFromGcloudWhenNothingElseNamesOne(t *testing.T) {
	withoutAnAmbientProject(t)

	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "gcloud"), []byte("#!/bin/sh\necho gcloud-config-prod\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	if got := targeted(t, providerkit.Options{"region": "europe-west1"}); got != "gcloud-config-prod" {
		t.Errorf("the run targets %q, want the project gcloud's config names", got)
	}
}

func TestAProjectNeitherNamedNorAmbientIsRefusedNamingWhereItIsRead(t *testing.T) {
	withoutAnAmbientProject(t)

	var refusal providerkit.Refusal
	_, err := gcp.New(context.Background(), providerkit.Options{"region": "europe-west1"})
	if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("New() with no project and nothing ambient = %v, want an %s refusal", err, providerkit.CodeInvalid)
	}
	for _, named := range []string{"project", "GOOGLE_CLOUD_PROJECT", "CLOUDSDK_CORE_PROJECT", "gcloud"} {
		if !strings.Contains(refusal.Message, named) {
			t.Errorf("New() refused with %q, want it to name %s among the places a project is read from", refusal.Message, named)
		}
	}
}

func withApplicationDefaultCredentials(t *testing.T, project string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	written, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"project_id":   project,
		"private_key":  string(block),
		"client_email": "deployer@" + project + ".iam.gserviceaccount.com",
		"token_uri":    "https://oauth2.googleapis.com/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "adc.json")
	if err := os.WriteFile(path, written, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
}

func TestAProjectNamedInTheEnvironmentBeatsTheOneTheCredentialsCarry(t *testing.T) {
	withoutAnAmbientProject(t)
	withApplicationDefaultCredentials(t, "credential-project")
	t.Setenv("GOOGLE_CLOUD_PROJECT", "ambient-prod")

	if got := targeted(t, providerkit.Options{"region": "europe-west1"}); got != "ambient-prod" {
		t.Errorf("the run targets %q, want the project the environment names: on GCE the credentials always carry the metadata project, and naming one is how an operator overrides it", got)
	}
}

func TestTheCloudSDKProjectBeatsTheOneTheCredentialsCarry(t *testing.T) {
	withoutAnAmbientProject(t)
	withApplicationDefaultCredentials(t, "credential-project")
	t.Setenv("CLOUDSDK_CORE_PROJECT", "sdk-prod")

	if got := targeted(t, providerkit.Options{"region": "europe-west1"}); got != "sdk-prod" {
		t.Errorf("the run targets %q, want the project CLOUDSDK_CORE_PROJECT names", got)
	}
}

func TestTheCredentialsAreReadWhenNothingAroundTheRunNamesAProject(t *testing.T) {
	withoutAnAmbientProject(t)
	withApplicationDefaultCredentials(t, "credential-project")

	if got := targeted(t, providerkit.Options{"region": "europe-west1"}); got != "credential-project" {
		t.Errorf("the run targets %q, want the project the credentials carry", got)
	}
}
