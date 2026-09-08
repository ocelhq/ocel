package discovery

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/nodeprotocol"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
)

type Launcher interface {
	Command(ctx context.Context, configDir string, root Root, serverURL string) (*exec.Cmd, error)
}

var launchers = map[Language]Launcher{}

func Run(ctx context.Context, configDir string, roots []Root, serverURL string, stdout, stderr io.Writer) error {
	commands, err := commandsFor(ctx, configDir, roots, serverURL)
	if err != nil {
		return err
	}

	safeStdout, safeStderr := nodeprotocol.SyncPair(stdout, stderr)
	for _, cmd := range commands {
		if err := runOne(ctx, cmd, safeStdout, safeStderr); err != nil {
			return err
		}
	}
	return postSync(ctx, serverURL)
}

func commandsFor(ctx context.Context, configDir string, roots []Root, serverURL string) ([]*exec.Cmd, error) {
	var jsRoots []Root
	var commands []*exec.Cmd

	for _, root := range roots {
		if root.Language == JS {
			jsRoots = append(jsRoots, root)
			continue
		}
		launcher, ok := launchers[root.Language]
		if !ok {
			return nil, fmt.Errorf("discovery: %s is a %s folder, and this build of ocel discovers only js folders", root.Dir, root.Language)
		}
		cmd, err := launcher.Command(ctx, configDir, root, serverURL)
		if err != nil {
			return nil, err
		}
		commands = append(commands, cmd)
	}

	entry, err := BundleRoots(configDir, jsRoots)
	if err != nil {
		return nil, err
	}
	return append([]*exec.Cmd{nodeCommand(ctx, entry, serverURL)}, commands...), nil
}

func BundleRoots(configDir string, roots []Root) (string, error) {
	var files []string
	for _, root := range roots {
		if root.Language != JS {
			continue
		}
		found, err := walkSourceFiles(root.Dir)
		if err != nil {
			return "", fmt.Errorf("discover resources: %w", err)
		}
		files = append(files, found...)
	}
	entry, err := Bundle(configDir, files)
	if err != nil {
		return "", fmt.Errorf("bundle discovery entrypoint: %w", err)
	}
	return entry, nil
}

func nodeCommand(ctx context.Context, entry, serverURL string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "node", "--enable-source-maps", entry)
	cmd.Env = append(os.Environ(), "OCEL_PHASE=discovery", "OCEL_DEV_SERVER="+serverURL)
	return cmd
}

func runOne(ctx context.Context, cmd *exec.Cmd, stdout, stderr io.Writer) error {
	cmd.Stderr = stderr

	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("discovery failed: %w", err)
	}

	proc := &nodeprotocol.Processor{Run: runtrace.FromContext(ctx), Forward: stdout}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("discovery failed: %w", err)
	}
	proc.Scan(ctx, pipe)
	runErr := cmd.Wait()

	if runErr != nil {
		proc.Abort()
		if msg := proc.Failure(); msg != "" {
			return fmt.Errorf("discovery failed (%w): %s", runErr, msg)
		}
		return fmt.Errorf("discovery failed: %w", runErr)
	}
	return nil
}

func postSync(ctx context.Context, serverURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(serverURL, "/")+"/sync", nil)
	if err != nil {
		return fmt.Errorf("discovery: sync failed: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("discovery: sync failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("discovery: sync failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
