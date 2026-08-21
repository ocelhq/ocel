package pulumi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/blang/semver"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

// DefaultVersion is the CLI the adapter installs when a Config names none. It
// tracks the automation-API version this module builds against: an engine and
// the library driving it disagreeing is a class of bug nobody should debug.
const DefaultVersion = "3.251.0"

const cacheDirName = ".ocel"

// command installs the pinned CLI once per Releaser and remembers the outcome,
// failure included. A second stack must not re-attempt a download that already
// failed, and must not race a first one that is still going.
func (a *Adapter) command(ctx context.Context, report providerkit.Reporter) (auto.PulumiCommand, error) {
	a.once.Do(func() {
		a.cmd, a.cmdErr = install(ctx, a.cfg.Version, a.cfg.Root, report)
	})
	return a.cmd, a.cmdErr
}

func install(ctx context.Context, pin, root string, report providerkit.Reporter) (auto.PulumiCommand, error) {
	if pin == "" {
		pin = DefaultVersion
	}
	version, err := semver.ParseTolerant(pin)
	if err != nil {
		return nil, providerkit.Refuse(providerkit.CodeInvalid, "%q is not a Pulumi version", pin)
	}
	if root == "" {
		root, err = installRoot(version)
		if err != nil {
			return nil, err
		}
	}

	opts := &auto.PulumiCommandOptions{Version: version, Root: root}

	// The probe is what tells an already-installed run from a first one, so the
	// user hears about a download only when there is one to wait for.
	if _, err := auto.NewPulumiCommand(opts); err != nil {
		report.Say(fmt.Sprintf("Downloading Pulumi runtime %s (one-time setup)…", version))
	}

	cmd, err := auto.InstallPulumiCommand(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("install Pulumi runtime %s: %w", version, err)
	}
	return cmd, nil
}

// installRoot keeps each pin in its own directory so changing the pin is an
// install and never an in-place overwrite of a version something else is using.
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
