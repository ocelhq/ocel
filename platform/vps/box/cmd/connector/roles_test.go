package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/proto/endpoint/v1/endpointv1connect"
	"github.com/ocelhq/ocel/pkg/proto/runner/v1/runnerv1connect"
	"github.com/ocelhq/ocel/pkg/runner"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

var connectorConfig = endpoint.Config{
	Console:        "https://console.example.com",
	EndpointID:     "endpoint-1",
	OrganizationID: "org-1",
}

var consoleTrust = boxapi.ConsoleTrust{
	Issuer:         "https://console.example.com",
	Audience:       "ocel-target:target-1",
	OrganizationID: "org-1",
	TargetID:       "target-1",
}

func boxIn(t *testing.T) boxFiles {
	t.Helper()
	root := t.TempDir()
	return boxFiles{
		signer:          []string{"false"},
		authorityConfig: filepath.Join(root, "ssh", "authority.json"),
		hostKeys:        filepath.Join(root, "sshd", "ssh_host_*_key.pub"),
		consoleTrust: func(tier environment.Tier) string {
			return filepath.Join(root, string(tier), "console-trust.json")
		},
		runnerConfig: filepath.Join(root, "runner.json"),
		runnerDir:    filepath.Join(root, "runner"),
	}
}

func written(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writtenJSON(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	written(t, path, encoded)
}

func withAuthority(t *testing.T, files boxFiles) {
	t.Helper()
	writtenJSON(t, files.authorityConfig, boxapi.AuthorityConfig{Principal: boxapi.DeployLogin})
}

func servicesOf(read roles) []string {
	return endpoint.Spec{Mounts: read.mounts()}.Services()
}

func TestABoxNoBootstrapTrustedServesNoCertificatesAndRunsNoJobs(t *testing.T) {
	read, err := readRoles(boxIn(t), connectorConfig, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if read.trust != nil || len(read.mounts()) != 0 {
		t.Errorf("roles = %+v, want none", read)
	}
}

func TestABoxWhoseBootstrapTrustsTheConsoleSignsCertificatesForThatTarget(t *testing.T) {
	files := boxIn(t)
	withAuthority(t, files)
	writtenJSON(t, files.consoleTrust(environment.TierProduction), consoleTrust)

	read, err := readRoles(files, connectorConfig, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := endpoint.CredentialTrust{Issuer: consoleTrust.Issuer, Audience: consoleTrust.Audience, TargetID: consoleTrust.TargetID}
	if read.trust == nil || *read.trust != want {
		t.Errorf("trust = %+v, want %+v", read.trust, want)
	}
	if !slices.Contains(servicesOf(read), endpointv1connect.SshCertificateServiceName) {
		t.Errorf("services = %v, want the certificate service", servicesOf(read))
	}
}

func certificatesRefused(t *testing.T, files boxFiles) string {
	t.Helper()
	var said bytes.Buffer
	read, err := readRoles(files, connectorConfig, &said)
	if err != nil {
		t.Fatalf("readRoles = %v, want the connector to start serving what it still can", err)
	}
	if read.trust != nil || read.authority != nil || slices.Contains(servicesOf(read), endpointv1connect.SshCertificateServiceName) {
		t.Errorf("roles = %+v, want no certificates served", read)
	}
	return said.String()
}

func TestATrustWithNoAuthorityServesNoCertificatesNamingTheBootstrapThatWritesIt(t *testing.T) {
	files := boxIn(t)
	writtenJSON(t, files.consoleTrust(environment.TierPreview), consoleTrust)

	if said := certificatesRefused(t, files); !strings.Contains(said, "ocel bootstrap preview") {
		t.Errorf("the connector said %q, want it to name ocel bootstrap preview", said)
	}
}

func TestATrustForAnotherOrganizationServesNoCertificatesAndKeepsTheRestUp(t *testing.T) {
	files := boxIn(t)
	withAuthority(t, files)
	foreign := consoleTrust
	foreign.OrganizationID = "org-2"
	writtenJSON(t, files.consoleTrust(environment.TierProduction), foreign)
	writtenJSON(t, files.runnerConfig, boxapi.RunnerConfig{URL: "https://box.example.com" + boxapi.ConnectorPath, Image: "ocel-job:1.2.3"})

	if said := certificatesRefused(t, files); !strings.Contains(said, "org-2") {
		t.Errorf("the connector said %q, want the foreign organization named", said)
	}
	var said bytes.Buffer
	read, err := readRoles(files, connectorConfig, &said)
	if err != nil {
		t.Fatal(err)
	}
	defer read.runner.Close()
	if !slices.Contains(servicesOf(read), runnerv1connect.RunnerServiceName) {
		t.Errorf("services = %v, want the runner still served", servicesOf(read))
	}
}

func TestTiersTrustingDifferentTargetsServeNoCertificates(t *testing.T) {
	files := boxIn(t)
	withAuthority(t, files)
	other := consoleTrust
	other.TargetID, other.Audience = "target-2", "ocel-target:target-2"
	writtenJSON(t, files.consoleTrust(environment.TierProduction), consoleTrust)
	writtenJSON(t, files.consoleTrust(environment.TierPreview), other)

	if said := certificatesRefused(t, files); said == "" {
		t.Error("the connector said nothing about the certificates it stopped serving")
	}
}

func TestTheHostKeysAreTheBoxsPublicHostKeysWithoutComments(t *testing.T) {
	files := boxIn(t)
	written(t, strings.Replace(files.hostKeys, "*", "ed25519", 1), []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl root@box\n"))

	keys, err := readHostKeys(files.hostKeys)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"}; !slices.Equal(keys, want) {
		t.Errorf("keys = %q, want %q", keys, want)
	}
}

func TestABoxWhoseConfigEnablesTheRunnerServesJobsAndTheirUploads(t *testing.T) {
	files := boxIn(t)
	writtenJSON(t, files.runnerConfig, boxapi.RunnerConfig{URL: "https://box.example.com" + boxapi.ConnectorPath, Image: "ocel-job:1.2.3"})

	read, err := readRoles(files, connectorConfig, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer read.runner.Close()
	services := servicesOf(read)
	for _, want := range []string{runnerv1connect.RunnerServiceName, runnerv1connect.JobAgentServiceName} {
		if !slices.Contains(services, want) {
			t.Errorf("services = %v, want %s", services, want)
		}
	}
	blobs := slices.IndexFunc(read.mounts(), func(mount endpoint.Mount) bool { return mount.SelfAuthenticated })
	if blobs < 0 {
		t.Error("no mount serves the uploads a signed URL authenticates")
	}
}

func TestAJobDrivesTheJobsDockerDaemonAndNeverTheHostsSocket(t *testing.T) {
	args := strings.Join(jobLauncher(boxapi.RunnerConfig{URL: "https://box.example.com" + boxapi.ConnectorPath, Image: "ocel-job:1.2.3"}).RunArgs(runner.Launch{JobID: "job_1"}), " ")
	for _, want := range []string{
		"--mount type=bind,source=" + boxapi.RunnerJobsDaemonDir + ",target=" + boxapi.RunnerJobsDaemonDir,
		"--env DOCKER_HOST=unix://" + boxapi.RunnerJobsDaemonSocket,
	} {
		if !strings.Contains(args, want) {
			t.Errorf("a job runs with %q, want %q", args, want)
		}
	}
	if strings.Contains(args, "/var/run/docker.sock") {
		t.Errorf("a job runs with %q, want the host's Docker socket kept from it", args)
	}
}

func TestTheConnectorSignsThroughTheRootHelperAndHandsItTheRequestOnStdin(t *testing.T) {
	signed, err := signThrough(context.Background(), []string{"/bin/sh", "-c", `read -r request; case "$request" in *'"principal":"ocel-deploy"'*) echo ssh-ed25519-cert-v01@openssh.com AAAA ;; *) echo refused >&2; exit 1 ;; esac`},
		boxapi.CertificateRequest{Principal: boxapi.DeployLogin, KeyID: "ocel-1 project-1 production"})
	if err != nil || signed != "ssh-ed25519-cert-v01@openssh.com AAAA" {
		t.Fatalf("signThrough = %q, %v, want the helper's certificate", signed, err)
	}
	if _, err := signThrough(context.Background(), []string{"/bin/sh", "-c", "echo 'ocel-live ssh-sign: the certificate asked for names \"root\"' >&2; exit 3"}, boxapi.CertificateRequest{}); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("err = %v, want the helper's own refusal", err)
	}
}
