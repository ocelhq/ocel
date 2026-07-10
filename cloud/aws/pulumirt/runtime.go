// Package pulumirt manages the Pulumi runtime the AWS provider drives: a
// pinned Pulumi CLI that Ocel installs and owns under the user's home, so
// users never install or manage Pulumi themselves. The first invocation
// downloads it (surfaced as visible, one-time progress); later invocations
// reuse the cached binary.
package pulumirt

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blang/semver"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
)

// pinnedPulumiVersion is the Pulumi CLI version the provider installs and
// requires. It tracks the Pulumi SDK this module builds against.
const pinnedPulumiVersion = "3.146.0"

// cacheDirName is the Ocel-owned directory (under the user's home) the pinned
// Pulumi CLI is installed into.
const cacheDirName = ".ocel"

// Ensure installs the pinned Pulumi CLI if it isn't already cached and
// returns a command handle bound to it. progress is called once with a
// human-readable notice when a download is about to happen; it may be nil.
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

	// If a matching CLI is already installed, NewPulumiCommand succeeds and no
	// download happens. Otherwise announce the one-time install before it runs.
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

// installRoot returns the versioned directory the pinned CLI lives in,
// creating its parent as needed: ~/.ocel/pulumi/<version>.
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
