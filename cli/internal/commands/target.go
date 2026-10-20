package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/project"
)

type CloudTarget struct {
	ConsoleURL  string
	Credentials console.Credentials
	Offer       console.CloudOffer
}

type Target struct {
	Project *project.Project
	Cloud   *CloudTarget
	Derived string
}

func (t Target) IsCloud() bool {
	return t.Cloud != nil
}

func (i Invocation) ResolveTarget(ctx context.Context, cwd string) (Target, error) {
	if i.BuilderJob != nil {
		cfg, err := i.LoadProject(ctx, cwd)
		return Target{Project: cfg}, err
	}
	configPath, err := project.FindConfig(cwd, i.ConfigPath())
	if err != nil {
		return Target{}, nil
	}
	if configPath != "" && (!project.IsTypeScript(configPath) || isNodeOnPath()) {
		cfg, err := i.LoadProject(ctx, cwd)
		if err != nil || cfg.HasProvider() {
			return Target{Project: cfg}, nil
		}
		cloud, err := i.cloudTarget(ctx)
		return Target{Project: cfg, Cloud: cloud}, err
	}

	cloud, err := i.cloudTarget(ctx)
	if err != nil || cloud == nil {
		return Target{}, err
	}
	if configPath == "" {
		dir, err := filepath.Abs(cwd)
		if err != nil {
			return Target{}, err
		}
		slug, err := deriveSlug(dir)
		if err != nil {
			return Target{}, err
		}
		return Target{Project: project.NewUnconfigured(dir, slug), Cloud: cloud, Derived: fmt.Sprintf("No %s here, so this runs as %s, named after this directory", english.Or([]string{project.DefaultFileName, project.YAMLFileName, project.TSFileName}), slug)}, nil
	}
	if mentionsProvider(configPath) {
		return Target{}, nil
	}
	dir := filepath.Dir(configPath)
	base := filepath.Base(configPath)
	if link, err := console.ReadLink(dir, cloud.ConsoleURL); err == nil && link != nil && project.DeriveSlug(link.ProjectName) != "" {
		slug := project.DeriveSlug(link.ProjectName)
		return Target{Project: project.NewUnconfigured(dir, slug), Cloud: cloud, Derived: fmt.Sprintf("%s needs node to read and node is not on PATH, so this runs as %s, the console project this directory is linked to", base, slug)}, nil
	}
	slug, err := deriveSlug(dir)
	if err != nil {
		return Target{}, err
	}
	return Target{Project: project.NewUnconfigured(dir, slug), Cloud: cloud, Derived: fmt.Sprintf("%s needs node to read and node is not on PATH, so this runs as %s, named after this directory", base, slug)}, nil
}

func (i Invocation) LoadTargetProject(ctx context.Context, cwd string) (*project.Project, error) {
	target, err := i.ResolveTarget(ctx, cwd)
	if err != nil {
		return nil, err
	}
	if target.IsCloud() || target.Project != nil && target.Project.HasProvider() {
		return target.Project, nil
	}
	return i.LoadProject(ctx, cwd)
}

func (i Invocation) RequireProvider(ctx context.Context, cfg *project.Project, command string) (*project.Provider, error) {
	if _, err := i.ResolveProvider(ctx, cfg, command, OpenOptions{}); err != nil {
		return nil, err
	}
	return cfg.Provider, nil
}

func (i Invocation) ResolveProvider(ctx context.Context, cfg *project.Project, command string, opts OpenOptions) (*CloudTarget, error) {
	if cfg.HasProvider() {
		return nil, nil
	}
	cloud, err := i.cloudTarget(ctx)
	switch {
	case cloud != nil && !opts.RunsOnCloud:
		return nil, NotOnCloud(command)
	case cloud != nil:
		return cloud, nil
	case err != nil && opts.RunsOnCloud:
		return nil, err
	}
	_, err = cfg.RequireProvider()
	return nil, err
}

func NotOnCloud(command string) error {
	return &clierror.Error{
		Code:  clierror.CodeCloudUnsupported,
		Cause: fmt.Errorf("`%s` does not run on Ocel Cloud yet: this project names no provider, so it deploys to Ocel Cloud through the console you are logged in to — name a provider in its config to run `%s` against your own account", command, command),
	}
}

func (i Invocation) cloudTarget(ctx context.Context) (*CloudTarget, error) {
	if i.Console.LoadCredentials == nil || i.CloudOffer == nil {
		return nil, nil
	}
	creds, err := i.Console.LoadCredentials()
	if err != nil || creds.AccessToken == "" {
		return nil, nil
	}
	offer, err := i.CloudOffer(ctx, creds)
	consoleURL := console.BaseURL(creds.APIURL)
	if err != nil {
		return nil, fmt.Errorf("no provider is named, and whether %s offers Ocel Cloud could not be read — run `ocel logout` to stop asking it: %w", consoleURL, err)
	}
	if offer == nil {
		return nil, nil
	}
	return &CloudTarget{ConsoleURL: consoleURL, Credentials: creds, Offer: *offer}, nil
}

func deriveSlug(dir string) (string, error) {
	slug := project.DeriveSlug(filepath.Base(dir))
	if err := project.ValidateSlug(slug); err != nil {
		return "", fmt.Errorf("no slug can be named after %s: %w — run `ocel init` to write a config that names one", dir, err)
	}
	return slug, nil
}

func isNodeOnPath() bool {
	_, err := exec.LookPath("node")
	return err == nil
}

func mentionsProvider(configPath string) bool {
	read, err := os.ReadFile(configPath)
	return err != nil || strings.Contains(string(read), "provider")
}
