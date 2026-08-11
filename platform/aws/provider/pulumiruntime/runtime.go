package pulumiruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blang/semver"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
)

const pinnedPulumiVersion = "3.146.0"

const cacheDirName = ".ocel"

func Ensure(ctx context.Context, progress func(string)) (auto.PulumiCommand, error) {
	version, err := semver.ParseTolerant(pinnedPulumiVersion)
	if err != nil {
		return nil, fmt.Errorf("parse pinned Pulumi version: %w", err)
	}

	root, err := installRoot(version)
	if err != nil {
		return nil, err
	}

	opts := &auto.PulumiCommandOptions{Version: version, Root: root}

	if _, err := auto.NewPulumiCommand(opts); err != nil {
		if progress != nil {
			progress(fmt.Sprintf("Downloading Pulumi runtime %s (one-time setup)…", pinnedPulumiVersion))
		}
	}

	cmd, err := auto.InstallPulumiCommand(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("install Pulumi runtime %s: %w", pinnedPulumiVersion, err)
	}
	return cmd, nil
}

func installRoot(version semver.Version) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	root := filepath.Join(home, cacheDirName, "pulumi", version.String())
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create Pulumi runtime dir: %w", err)
	}
	return root, nil
}
