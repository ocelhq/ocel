package commands

import (
	"cmp"
	"context"
	"errors"
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
	configPath, err := project.FindConfig(cwd, i.ConfigPath())
	if err != nil {
		return Target{}, err
	}
	if configPath != "" && project.IsTypeScript(configPath) && !isNodeOnPath() && !mentionsProvider(configPath) {
		if target, err := i.resolveLinkedWithoutNode(ctx, configPath); err != nil || target.IsCloud() {
			return target, err
		}
	}
	cfg, err := i.LoadProject(ctx, cwd)
	var missing project.NoConfigError
	if errors.As(err, &missing) {
		return i.resolveUnconfigured(ctx, cwd, err)
	}
	if err != nil || cfg.HasProvider() {
		return Target{Project: cfg}, err
	}
	cloud, err := i.cloudTarget(ctx)
	return Target{Project: cfg, Cloud: cloud}, err
}

func (i Invocation) resolveUnconfigured(ctx context.Context, cwd string, missing error) (Target, error) {
	cloud, err := i.cloudTarget(ctx)
	if err != nil || cloud == nil {
		return Target{}, cmp.Or(err, missing)
	}
	dir, err := filepath.Abs(cwd)
	if err != nil {
		return Target{}, err
	}
	slug := project.DeriveSlug(filepath.Base(dir))
	if err := project.ValidateSlug(slug); err != nil {
		return Target{}, fmt.Errorf("no slug can be named after %s: %w — run `ocel init` to write a config that names one", dir, err)
	}
	derived := fmt.Sprintf("No %s here, so this runs as %s, named after this directory", english.Or([]string{project.DefaultFileName, project.YAMLFileName, project.TSFileName}), slug)
	return Target{Project: project.NewUnconfigured(dir, slug), Cloud: cloud, Derived: derived}, nil
}

func (i Invocation) resolveLinkedWithoutNode(ctx context.Context, configPath string) (Target, error) {
	cloud, err := i.cloudTarget(ctx)
	if err != nil || cloud == nil {
		return Target{}, err
	}
	dir := filepath.Dir(configPath)
	link, err := console.ReadLink(dir, cloud.ConsoleURL)
	if err != nil || link == nil {
		return Target{}, err
	}
	slug := project.DeriveSlug(link.ProjectName)
	if project.ValidateSlug(slug) != nil {
		return Target{}, nil
	}
	derived := fmt.Sprintf("%s needs node to read and node is not on PATH, so this runs as %s, the console project this directory is linked to", filepath.Base(configPath), slug)
	return Target{Project: project.NewUnconfigured(dir, slug), Cloud: cloud, Derived: derived}, nil
}

func (i Invocation) RequireProvider(ctx context.Context, cfg *project.Project, command string) (*project.Provider, error) {
	if cfg.HasProvider() {
		return cfg.Provider, nil
	}
	if cloud, _ := i.cloudTarget(ctx); cloud != nil {
		return nil, NotOnCloud(command)
	}
	return cfg.RequireProvider()
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

func isNodeOnPath() bool {
	_, err := exec.LookPath("node")
	return err == nil
}

func mentionsProvider(configPath string) bool {
	read, err := os.ReadFile(configPath)
	return err != nil || strings.Contains(string(read), "provider")
}
