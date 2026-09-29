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

	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/nodeprotocol"
	"github.com/ocelhq/ocel/cli/internal/runtrace"
	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/constants"
)

type Server struct {
	URL   string
	Token string
}

func (s Server) Env() []string {
	return []string{
		constants.PhaseEnvName + "=discovery",
		constants.DevServerEnvName + "=" + s.URL,
		constants.DevServerTokenEnvName + "=" + s.Token,
	}
}

type rootCommand func(ctx context.Context, configDir string, root Root, server Server) (*exec.Cmd, error)

var rootCommands = map[language.Language]rootCommand{language.Go: goCommand, language.Python: pythonCommand, language.Rust: rustCommand}

type Programs struct {
	Roots []Root
	entry string
}

func Prepare(configDir string, roots []Root) (Programs, error) {
	entry, err := BundleRoots(configDir, roots)
	if err != nil {
		return Programs{}, err
	}
	return Programs{Roots: roots, entry: entry}, nil
}

func (p Programs) Entry() string { return p.entry }

func Run(ctx context.Context, configDir string, programs Programs, server Server, stdout, stderr io.Writer) error {
	commands, err := commandsFor(ctx, configDir, programs.Roots, programs.entry, server)
	if err != nil {
		return err
	}

	safeStdout, safeStderr := nodeprotocol.SyncPair(stdout, stderr)
	for _, cmd := range commands {
		if err := runOne(ctx, cmd, safeStdout, safeStderr); err != nil {
			return err
		}
	}
	return postSync(ctx, server)
}

func commandsFor(ctx context.Context, configDir string, roots []Root, entry string, server Server) ([]*exec.Cmd, error) {
	var commands []*exec.Cmd
	if entry != "" {
		commands = append(commands, nodeCommand(ctx, entry, server))
	}

	for _, root := range roots {
		if root.Language == language.JS {
			continue
		}
		command, ok := rootCommands[root.Language]
		if !ok {
			return nil, fmt.Errorf("discovery: %s is a %s folder, and this build of ocel discovers only %s folders", root.Dir, root.Language, discoverable())
		}
		cmd, err := command(ctx, configDir, root, server)
		if err != nil {
			return nil, err
		}
		commands = append(commands, cmd)
	}
	return commands, nil
}

func discoverable() string {
	languages := []string{string(language.JS)}
	for written := range rootCommands {
		languages = append(languages, string(written))
	}
	slices.Sort(languages)
	return english.And(languages)
}

func BundleRoots(configDir string, roots []Root) (string, error) {
	var files []string
	var js bool
	for _, root := range roots {
		if root.Language != language.JS {
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

func nodeCommand(ctx context.Context, entry string, server Server) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "node", "--enable-source-maps", entry)
	cmd.Env = append(os.Environ(), server.Env()...)
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
	_ = proc.Abort(ctx)

	if runErr != nil {
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

func postSync(ctx context.Context, server Server) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(server.URL, "/")+"/sync", nil)
	if err != nil {
		return fmt.Errorf("discovery: sync failed: %w", err)
	}
	req.Header.Set("Authorization", channel.FormatAuthHeader(server.Token))
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
