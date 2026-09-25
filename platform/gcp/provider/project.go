package gcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"golang.org/x/oauth2/google"
)

const (
	projectVariable  = "GOOGLE_CLOUD_PROJECT"
	cloudSDKVariable = "CLOUDSDK_CORE_PROJECT"
)

const unsetProject = "(unset)"

func namedProject(named string) string {
	for _, held := range []string{named, os.Getenv(projectVariable), os.Getenv(cloudSDKVariable)} {
		if project := strings.TrimSpace(held); project != "" {
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
		return "", providerkit.Refuse(providerkit.CodeInvalid,
			"option %q names no Google Cloud project and nothing around this run names one either: "+
				"not the application default credentials, not %s, not %s, and gcloud could not say what its config holds: %s.\n"+
				"Name it in the options, or run `gcloud config set project <id>`",
			"project", projectVariable, cloudSDKVariable, err)
	}
	return "", providerkit.Refuse(providerkit.CodeInvalid,
		"option %q names no Google Cloud project and nothing around this run names one either: "+
			"not the application default credentials, not %s, not %s, and not gcloud's own config.\n"+
			"Name it in the options, or run `gcloud config set project <id>`",
		"project", projectVariable, cloudSDKVariable)
}

func unnamedProject() error {
	return providerkit.Refuse(providerkit.CodeInvalid,
		"option %q names no Google Cloud project and neither %s nor %s does. "+
			"A scan reads no credentials and runs no gcloud to find one, so name it in the options or in one of them",
		"project", projectVariable, cloudSDKVariable)
}

func credentialProject(ctx context.Context) string {
	found, err := google.FindDefaultCredentials(ctx, cloudPlatformScope)
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

func (p *Provider) stood(ctx context.Context) (*clients, error) {
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
	names := Names{namespace: p.namespace, project: project}
	if err := names.fit(); err != nil {
		return nil, err
	}
	p.resolved = &clients{Names: names, region: p.options.Region, endpoint: p.endpoint}
	return p.resolved, nil
}

func (p *Provider) Names(ctx context.Context) (Names, error) {
	held, err := p.stood(ctx)
	if err != nil {
		return Names{}, err
	}
	return held.Names, nil
}

func (p *Provider) Project(ctx context.Context) (string, error) {
	names, err := p.Names(ctx)
	if err != nil {
		return "", err
	}
	return names.project, nil
}

func (p *Provider) named() (Names, error) {
	if p.options.Project == "" {
		return Names{}, unnamedProject()
	}
	return Names{namespace: p.namespace, project: p.options.Project}, nil
}

func (p *Provider) emulated() bool { return p.endpoint != "" }

func (p *Provider) Region() string { return p.options.Region }
