package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runner"
	"github.com/ocelhq/ocel/platform/vps/box/certificateauthority"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

type boxFiles struct {
	signer          []string
	authorityConfig string
	hostKeys        string
	consoleTrust    func(environment.Tier) string
	runnerConfig    string
	runnerDir       string
}

func installedBoxFiles() boxFiles {
	return boxFiles{
		signer:          boxapi.SignArgv(),
		authorityConfig: boxapi.AuthorityConfigPath,
		hostKeys:        boxapi.HostKeysPattern,
		consoleTrust:    boxapi.ConsoleTrustPath,
		runnerConfig:    boxapi.RunnerConfigPath,
		runnerDir:       boxapi.RunnerDir,
	}
}

type roles struct {
	trust     *endpoint.CredentialTrust
	authority *certificateauthority.Authority
	runner    *runner.Server
}

func readRoles(files boxFiles, config endpoint.Config, log io.Writer) (roles, error) {
	var read roles
	trust, authority, err := readCertificates(files, config)
	if err != nil {
		fmt.Fprintf(log, "ocel connector: serving no ssh certificates: %s\n", err)
	} else {
		read.trust, read.authority = trust, authority
	}
	if read.runner, err = readRunner(files, config); err != nil {
		return roles{}, err
	}
	return read, nil
}

func readCertificates(files boxFiles, config endpoint.Config) (*endpoint.CredentialTrust, *certificateauthority.Authority, error) {
	trust, tiers, err := readConsoleTrust(files, config)
	if err != nil || trust == nil {
		return nil, nil, err
	}
	authority, err := readAuthority(files, tiers)
	if err != nil {
		return nil, nil, err
	}
	return trust, authority, nil
}

func (r roles) mounts() []endpoint.Mount {
	var mounts []endpoint.Mount
	if r.authority != nil {
		mounts = append(mounts, r.authority.Mount())
	}
	if r.runner != nil {
		for _, handler := range []func() (string, http.Handler){r.runner.RunnerServiceHandler, r.runner.JobAgentServiceHandler} {
			path, served := handler()
			mounts = append(mounts, endpoint.Mount{Path: path, Handler: served})
		}
		path, blobs := r.runner.BlobsHandler()
		mounts = append(mounts, endpoint.Mount{Path: path, Handler: blobs, SelfAuthenticated: true})
	}
	return mounts
}

func readConsoleTrust(files boxFiles, config endpoint.Config) (*endpoint.CredentialTrust, []environment.Tier, error) {
	var (
		agreed *boxapi.ConsoleTrust
		tiers  []environment.Tier
	)
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		path := files.consoleTrust(tier)
		var trust boxapi.ConsoleTrust
		found, err := readJSON(path, &trust)
		if err != nil {
			return nil, nil, err
		}
		if !found {
			continue
		}
		if trust.Issuer == "" || trust.Audience == "" || trust.TargetID == "" {
			return nil, nil, fmt.Errorf("%s names no issuer, audience or target: run `%s` again", path, provider.BootstrapCommand(tier))
		}
		if trust.OrganizationID != config.OrganizationID {
			return nil, nil, fmt.Errorf("%s trusts the console for organization %s, and this connector serves organization %s: bootstrap the box and add the connector from the same organization", path, trust.OrganizationID, config.OrganizationID)
		}
		if agreed != nil && *agreed != trust {
			return nil, nil, fmt.Errorf("the production and preview bootstraps trust different console targets: run `%s` and `%s` again", provider.BootstrapCommand(environment.TierProduction), provider.BootstrapCommand(environment.TierPreview))
		}
		agreed = &trust
		tiers = append(tiers, tier)
	}
	if agreed == nil {
		return nil, nil, nil
	}
	return &endpoint.CredentialTrust{Issuer: agreed.Issuer, Audience: agreed.Audience, TargetID: agreed.TargetID}, tiers, nil
}

func readAuthority(files boxFiles, tiers []environment.Tier) (*certificateauthority.Authority, error) {
	var config boxapi.AuthorityConfig
	found, err := readJSON(files.authorityConfig, &config)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("the box trusts the console and has no certificate authority at %s: run `%s` again", files.authorityConfig, provider.BootstrapCommand(tiers[0]))
	}
	signer := files.signer
	return certificateauthority.New(certificateauthority.Config{
		Principal: config.Principal,
		Sign: func(ctx context.Context, request boxapi.CertificateRequest) (string, error) {
			return signThrough(ctx, signer, request)
		},
		HostKeys: func() ([]string, error) { return readHostKeys(files.hostKeys) },
		Tiers:    tiers,
	})
}

func signThrough(ctx context.Context, signer []string, request boxapi.CertificateRequest) (string, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, signer[0], signer[1:]...)
	cmd.Stdin = bytes.NewReader(encoded)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		said := strings.TrimSpace(stderr.String())
		if said == "" {
			said = err.Error()
		}
		return "", errors.New(said)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func readHostKeys(pattern string) ([]string, error) {
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		fields := strings.Fields(string(raw))
		if len(fields) < 2 {
			continue
		}
		keys = append(keys, fields[0]+" "+fields[1])
	}
	return keys, nil
}

func readRunner(files boxFiles, config endpoint.Config) (*runner.Server, error) {
	var settings boxapi.RunnerConfig
	found, err := readJSON(files.runnerConfig, &settings)
	if err != nil || !found {
		return nil, err
	}
	if settings.URL == "" || settings.Image == "" {
		return nil, fmt.Errorf("%s names no URL or job image: bootstrap the box again", files.runnerConfig)
	}
	return runner.New(runner.Config{
		ID:       config.EndpointID,
		Dir:      files.runnerDir,
		URL:      settings.URL,
		Launcher: jobLauncher(settings),
	})
}

func jobLauncher(settings boxapi.RunnerConfig) *runner.Docker {
	return &runner.Docker{
		Image:     settings.Image,
		RunnerURL: settings.URL,
		Mounts: []string{
			"type=volume,source=" + boxapi.RunnerCache + ",target=" + jobCache,
			"type=bind,source=" + boxapi.RunnerJobsDaemonDir + ",target=" + boxapi.RunnerJobsDaemonDir,
		},
		Env: append(cachedTools(), "DOCKER_HOST=unix://"+boxapi.RunnerJobsDaemonSocket),
		Run: func(ctx context.Context, env []string, args ...string) (string, error) {
			return runHelper(ctx, env, args)
		},
	}
}

const jobCache = "/root/.cache"

func cachedTools() []string {
	return []string{
		"npm_config_cache=" + jobCache + "/npm",
		"npm_config_store_dir=" + jobCache + "/pnpm",
		"BUN_INSTALL_CACHE_DIR=" + jobCache + "/bun",
		"GOMODCACHE=" + jobCache + "/go-mod",
	}
}

func runHelper(ctx context.Context, env, args []string) (string, error) {
	argv := boxapi.RunnerDockerArgv(args...)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func readJSON(path string, into any) (bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	return true, nil
}
