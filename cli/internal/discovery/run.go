package discovery

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/nodeprotocol"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
)

type Launcher interface {
	Command(ctx context.Context, configDir string, root Root, serverURL string) (*exec.Cmd, error)
}

var launchers = map[Language]Launcher{Go: goLauncher{}, Python: pythonLauncher{}, Rust: rustLauncher{}}

type Prepared struct {
	Roots []Root
	entry string
}

func Prepare(configDir string, roots []Root) (Prepared, error) {
	entry, err := BundleRoots(configDir, roots)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{Roots: roots, entry: entry}, nil
}

func (p Prepared) Entry() string { return p.entry }

func Run(ctx context.Context, configDir string, prepared Prepared, serverURL string, stdout, stderr io.Writer) error {
	commands, err := commandsFor(ctx, configDir, prepared.Roots, prepared.entry, serverURL)
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

func commandsFor(ctx context.Context, configDir string, roots []Root, entry, serverURL string) ([]*exec.Cmd, error) {
	var commands []*exec.Cmd
	if entry != "" {
		commands = append(commands, nodeCommand(ctx, entry, serverURL))
	}

	for _, root := range roots {
		if root.Language == JS {
			continue
		}
		launcher, ok := launchers[root.Language]
		if !ok {
			return nil, fmt.Errorf("discovery: %s is a %s folder, and this build of ocel discovers only %s folders", root.Dir, root.Language, discoverable())
		}
		cmd, err := launcher.Command(ctx, configDir, root, serverURL)
		if err != nil {
			return nil, err
		}
		commands = append(commands, cmd)
	}
	return commands, nil
}

func discoverable() string {
	languages := []string{string(JS)}
	for language := range launchers {
		languages = append(languages, string(language))
	}
	slices.Sort(languages)
	return listed(languages)
}

func listed(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func BundleRoots(configDir string, roots []Root) (string, error) {
	var files []string
	var js bool
	for _, root := range roots {
		if root.Language != JS {
			continue
		}
		js = true
		found, err := walkSourceFiles(root.Dir)
		if err != nil {
			return "", fmt.Errorf("discover resources: %w", err)
		}
		files = append(files, found...)
	}
	if !js {
		return "", nil
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

const stderrTailBytes = 4 << 10

type tailWriter struct {
	limit int
	buf   []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if extra := len(t.buf) - t.limit; extra > 0 {
		t.buf = t.buf[extra:]
	}
	return len(p), nil
}

func (t *tailWriter) String() string { return strings.TrimSpace(string(t.buf)) }

func runOne(ctx context.Context, cmd *exec.Cmd, stdout, stderr io.Writer) error {
	tail := &tailWriter{limit: stderrTailBytes}
	cmd.Stderr = io.MultiWriter(stderr, tail)

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
		if msg := tail.String(); msg != "" {
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
