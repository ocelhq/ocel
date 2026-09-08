package gcp_test

import (
	"context"
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
