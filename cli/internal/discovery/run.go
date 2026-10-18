package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/workdir"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/cli/internal/english"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/nodeprotocol"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
)

type Server struct {
	URL   string
	Token string
}

func (s Server) Env() []string {
	return []string{
		processenv.PhaseEnvVar + "=discovery",
		processenv.DevServerEnvVar + "=" + s.URL,
		processenv.DevServerTokenEnvVar + "=" + s.Token,
	}
}

type programCommands func(ctx context.Context, configDir string, roots []Root, server Server) ([]*exec.Cmd, error)

var languageCommands = map[language.Language]programCommands{language.Go: goCommands, language.Python: pythonCommands, language.Rust: rustCommands}

const entryFileName = "entry.mjs"

type Programs struct {
	Roots  []Root
	bundle []byte
	run    *workdir.Run
}

func Prepare(configDir string, roots []Root) (Programs, error) {
	run := workdir.NewRun()
	bundle, err := bundleRoots(configDir, roots, run.Path(configDir, entryFileName))
	if err != nil {
		return Programs{}, err
	}
	return Programs{Roots: roots, bundle: bundle, run: run}, nil
}

func (p Programs) Fingerprint() string {
	if p.bundle == nil {
		return ""
	}
	for _, root := range p.Roots {
		if root.Language != language.JS {
			return ""
		}
	}
	sum := sha256.Sum256(p.bundle)
	return hex.EncodeToString(sum[:])
}

func Run(ctx context.Context, configDir string, programs Programs, server Server, stdout, stderr io.Writer) (err error) {
	defer func() {
		if programs.run != nil {
			err = errors.Join(err, programs.run.Remove())
		}
	}()
	commands, err := commandsFor(ctx, configDir, programs, server)
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

func commandsFor(ctx context.Context, configDir string, programs Programs, server Server) ([]*exec.Cmd, error) {
	var commands []*exec.Cmd
	if programs.bundle != nil {
		entry, err := programs.run.Write(configDir, entryFileName, programs.bundle)
		if err != nil {
			return nil, fmt.Errorf("discovery: %w", err)
		}
		commands = append(commands, nodeCommand(ctx, entry, server))
	}

	var written []language.Language
	byLanguage := map[language.Language][]Root{}
	for _, root := range programs.Roots {
		if root.Language == language.JS {
			continue
		}
		if _, ok := languageCommands[root.Language]; !ok {
			return nil, fmt.Errorf("discovery: %s is a %s folder, and this build of ocel discovers only %s folders", root.Dir, root.Language, discoverable())
		}
		if _, seen := byLanguage[root.Language]; !seen {
			written = append(written, root.Language)
		}
		byLanguage[root.Language] = append(byLanguage[root.Language], root)
	}
	for _, lang := range written {
		found, err := languageCommands[lang](ctx, configDir, byLanguage[lang], server)
		if err != nil {
			return nil, err
		}
		commands = append(commands, found...)
	}
	return commands, nil
}

func discoverable() string {
	languages := []string{string(language.JS)}
	for written := range languageCommands {
		languages = append(languages, string(written))
	}
	slices.Sort(languages)
	return english.And(languages)
}

func bundleRoots(configDir string, roots []Root, outfile string) ([]byte, error) {
	var files []string
	var js bool
	for _, root := range roots {
		if root.Language != language.JS {
			continue
		}
		js = true
		found, err := walkSourceFiles(root.Dir)
		if err != nil {
			return nil, fmt.Errorf("discover resources: %w", err)
		}
		files = append(files, found...)
	}
	if !js {
		return nil, nil
	}

	bundle, err := Bundle(configDir, files, outfile)
	if err != nil {
		return nil, fmt.Errorf("bundle discovery entrypoint: %w", err)
	}
	return bundle, nil
}

func nodeCommand(ctx context.Context, entry string, server Server) *exec.Cmd {
	cmd := exec.Command("node", "--enable-source-maps", entry)
	cmd.Env = append(os.Environ(), server.Env()...)
	return cmd
}

const stderrTailBytes = 4 << 10

func runOne(ctx context.Context, cmd *exec.Cmd, stdout, stderr io.Writer) error {
	tail := &childprocess.Tail{Limit: stderrTailBytes}
	cmd.Stderr = io.MultiWriter(stderr, tail)

	reader, writer, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("discovery failed: %w", err)
	}
	cmd.Stdout = writer

	proc := &nodeprotocol.Processor{Span: run.SpanFromContext(ctx), Forward: stdout}

	child, err := childprocess.Start(ctx, cmd, childprocess.Options{})
	_ = writer.Close()
	if err != nil {
		_ = reader.Close()
		return fmt.Errorf("discovery failed: %w", err)
	}
	proc.Scan(ctx, reader)
	_ = reader.Close()
	runErr := child.Wait()
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
	req.Header.Set("Authorization", localrpc.FormatAuthHeader(server.Token))
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
