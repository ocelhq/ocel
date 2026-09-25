package localsource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ocelhq/ocel/cli/internal/dotenv"
	"github.com/ocelhq/ocel/pkg/providerkit/envsource"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

const (
	runWindow    = 2 * time.Minute
	outputLimit  = 1 << 20
	stderrTail   = 2048
	tokenEnvVar  = "INFISICAL_TOKEN"
	infisicalCLI = "infisical"
)

func Version(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:8])
}

func FolderArgument(folder string) string {
	if folder == "" {
		return "/"
	}
	return folder
}

type execSource struct {
	options envsource.ExecOptions
	dir     string
}

func Exec(options envsource.ExecOptions, dir string) envsource.Source {
	return execSource{options: options, dir: dir}
}

func Dev(descriptor envsource.Descriptor, dir string, lookupEnv func(string) (string, bool)) (envsource.Source, error) {
	switch descriptor.Kind {
	case envsource.Exec:
		return Exec(*descriptor.Exec, dir), nil
	case envsource.Infisical:
		return DevInfisical(*descriptor.Infisical, lookupEnv, dir)
	}
	return nil, nil
}

func (execSource) ID() string { return string(envsource.Exec) }

func (execSource) Capabilities() envsource.Caps { return envsource.Caps{Read: true, List: true} }

func (s execSource) Resolve(ctx context.Context, folders []string) (map[values.Cell]envsource.Resolved, error) {
	out := map[values.Cell]envsource.Resolved{}
	for _, folder := range folders {
		argv := make([]string, len(s.options.Command))
		for i, arg := range s.options.Command {
			argv[i] = strings.ReplaceAll(arg, envsource.FolderPlaceholder, FolderArgument(folder))
		}
		printed, err := run(ctx, s.dir, argv)
		if err != nil {
			return nil, err
		}
		parsed, err := parse(printed, s.options.Format, argv[0])
		if err != nil {
			return nil, err
		}
		for key, value := range parsed {
			if value == "" {
				continue
			}
			out[values.Cell{Folder: folder, Key: key}] = envsource.Resolved{Value: []byte(value), Version: Version([]byte(value))}
		}
	}
	return out, nil
}

func (execSource) Put(context.Context, values.Cell, []byte, string) error {
	return envsource.ErrReadOnly
}

func (execSource) Link(values.Cell) string { return "" }

func run(ctx context.Context, dir string, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("the env source's exec names no command")
	}
	running, cancel := context.WithTimeout(ctx, runWindow)
	defer cancel()
	cmd := exec.CommandContext(running, argv[0], argv[1:]...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limited{buffer: &stdout, left: outputLimit}
	cmd.Stderr = &limited{buffer: &stderr, left: stderrTail}
	if err := cmd.Run(); err != nil {
		said := strings.TrimSpace(stderr.String())
		if said != "" {
			said = ": " + said
		}
		return nil, fmt.Errorf("the env source's command %s failed (%w)%s", argv[0], err, said)
	}
	return stdout.Bytes(), nil
}

type limited struct {
	buffer *bytes.Buffer
	left   int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.left > 0 {
		take := min(len(p), l.left)
		l.buffer.Write(p[:take])
		l.left -= take
	}
	return len(p), nil
}

func parse(printed []byte, format envsource.Format, command string) (map[string]string, error) {
	switch format {
	case envsource.FormatDotenv:
		file, err := dotenv.Parse(bytes.NewReader(printed))
		if err != nil {
			return nil, fmt.Errorf("read what %s printed: %w", command, err)
		}
		return file.Values, nil
	case envsource.FormatJSON:
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(printed, &raw); err != nil {
			return nil, fmt.Errorf("%s printed no JSON object of names to values: %w", command, err)
		}
		out := make(map[string]string, len(raw))
		for key, held := range raw {
			var value string
			if err := json.Unmarshal(held, &value); err != nil {
				return nil, fmt.Errorf("%s printed %s as something other than text; quote it", command, key)
			}
			out[key] = value
		}
		return out, nil
	}
	return nil, fmt.Errorf("an env source's exec prints json or dotenv, not %q", format)
}
