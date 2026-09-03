package pulumi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/blang/semver"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const PinnedVersion = "3.146.0"

const cacheDirName = ".ocel"

var pinned installer

func Install(ctx context.Context, report providerkit.Reporter) error {
	_, err := pinned.install(ctx, report)
	return err
}

type installer struct {
	once    sync.Once
	command auto.PulumiCommand
	err     error
}

func (i *installer) install(ctx context.Context, report providerkit.Reporter) (auto.PulumiCommand, error) {
	i.once.Do(func() { i.command, i.err = install(ctx, report) })
	return i.command, i.err
}

func install(ctx context.Context, report providerkit.Reporter) (auto.PulumiCommand, error) {
	version, err := semver.ParseTolerant(PinnedVersion)
	if err != nil {
		return nil, fmt.Errorf("parse pinned Pulumi version: %w", err)
	}

	root, err := installRoot(version)
	if err != nil {
		return nil, err
	}
	opts := &auto.PulumiCommandOptions{Version: version, Root: root}
	if command, err := auto.NewPulumiCommand(opts); err == nil {
		return command, nil
	}
	if report != nil {
		report.Say(fmt.Sprintf("Downloading Pulumi runtime %s (one-time setup)…", PinnedVersion))
	}

	staging := root + "-" + strconv.Itoa(os.Getpid())
	defer func() { _ = os.RemoveAll(staging) }()
	if _, err := auto.InstallPulumiCommand(ctx, &auto.PulumiCommandOptions{Version: version, Root: staging}); err != nil {
		return nil, fmt.Errorf("install Pulumi runtime %s: %w", PinnedVersion, err)
	}
	if err := settle(staging, root); err != nil {
		return nil, fmt.Errorf("install Pulumi runtime %s: %w", PinnedVersion, err)
	}
	command, err := auto.NewPulumiCommand(opts)
	if err != nil {
		return nil, fmt.Errorf("install Pulumi runtime %s into %s: %w", PinnedVersion, root, err)
	}
	return command, nil
}

func settle(staging, root string) error {
	err := os.Rename(staging, root)
	if err == nil {
		return nil
	}
	if _, stat := os.Stat(root); stat == nil {
		return nil
	}
	return err
}

func installRoot(version semver.Version) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	root := filepath.Join(home, cacheDirName, "pulumi", version.String())
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return "", fmt.Errorf("create Pulumi runtime dir: %w", err)
	}
	return root, nil
}
