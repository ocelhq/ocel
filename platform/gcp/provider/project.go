package gcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/oauth2/google"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

const (
	projectVariable  = "GOOGLE_CLOUD_PROJECT"
	cloudSDKVariable = "CLOUDSDK_CORE_PROJECT"
)

const unsetProject = "(unset)"

func namedProject(named string) string {
	for _, candidate := range []string{named, os.Getenv(projectVariable), os.Getenv(cloudSDKVariable)} {
		if project := strings.TrimSpace(candidate); project != "" {
			return project
		}
	}
	return ""
}

func ambientProject(ctx context.Context) (string, error) {
	if project := strings.TrimSpace(credentialProject(ctx)); project != "" {
		return project, nil
	}
	project, err := gcloudProject(ctx)
	if project != "" {
		return project, nil
	}
	if err != nil {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"option %q names no Google Cloud project and nothing around this run names one either: "+
				"not the application default credentials, not %s, not %s, and gcloud could not say which project its config sets: %s.\n"+
				"Name it in the options, or run `gcloud config set project <id>`",
			"project", projectVariable, cloudSDKVariable, err)
	}
	return "", refusal.Refuse(refusal.CodeInvalid,
		"option %q names no Google Cloud project and nothing around this run names one either: "+
			"not the application default credentials, not %s, not %s, and not gcloud's own config.\n"+
			"Name it in the options, or run `gcloud config set project <id>`",
		"project", projectVariable, cloudSDKVariable)
}

func credentialProject(ctx context.Context) string {
	found, err := google.FindDefaultCredentials(ctx, gcloud.CloudPlatformScope)
	if err != nil {
		return ""
	}
	return found.ProjectID
}

func gcloudProject(ctx context.Context) (string, error) {
	if _, err := exec.LookPath("gcloud"); err != nil {
		return "", nil
	}
	said, err := exec.CommandContext(ctx, "gcloud", "config", "get-value", "project").Output()
	if err != nil {
		var exited *exec.ExitError
		if errors.As(err, &exited) && len(bytes.TrimSpace(exited.Stderr)) > 0 {
			return "", fmt.Errorf("`gcloud config get-value project` failed: %s", bytes.TrimSpace(exited.Stderr))
		}
		return "", fmt.Errorf("`gcloud config get-value project` failed: %w", err)
	}
	project := strings.TrimSpace(string(said))
	if project == unsetProject {
		return "", nil
	}
	return project, nil
}

func (p *Provider) openClients(ctx context.Context) (*gcloud.Clients, error) {
	opened, err := p.resolveClients(ctx)
	if err != nil || opened.Emulated() || p.skipChecks {
		return opened, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.regionChecked {
		if err := opened.RefuseRegionNotServed(ctx); err != nil {
			return nil, err
		}
		p.regionChecked = true
	}
	return opened, nil
}

func (p *Provider) resolveClients(ctx context.Context) (*gcloud.Clients, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.resolved != nil {
		return p.resolved, nil
	}
	project := p.options.Project
	if project == "" {
		ambient, err := ambientProject(ctx)
		if err != nil {
			return nil, err
		}
		project = ambient
	}
	names := names.New(p.namespace, project)
	p.resolved = &gcloud.Clients{Names: names, Region: p.options.Region, Endpoint: p.endpoint, TagEndpoint: p.tagEndpoint, Records: p.KeyValues()}
	return p.resolved, nil
}

func (p *Provider) Names(ctx context.Context) (names.Names, error) {
	resolved, err := p.resolveClients(ctx)
	if err != nil {
		return names.Names{}, err
	}
	return resolved.Names, nil
}

func (p *Provider) Project(ctx context.Context) (string, error) {
	names, err := p.Names(ctx)
	if err != nil {
		return "", err
	}
	return names.Project(), nil
}

func (p *Provider) emulated() bool { return p.endpoint != "" }

func (p *Provider) Region() string { return p.options.Region }

func (p *Provider) services() *run.Services {
	return &run.Services{OpenClients: p.openClients, Records: p.KeyValues()}
}
