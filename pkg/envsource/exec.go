package envsource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ocelhq/ocel/pkg/dotenv"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

const (
	execKind           = "exec"
	commandTimeout     = 2 * time.Minute
	commandOutputBytes = 1 << 20
	commandStderrBytes = 2048
	rootFolderArgument = "/"
)

type Format string

const (
	FormatJSON   Format = "json"
	FormatDotenv Format = "dotenv"
)

const FolderPlaceholder = "{folder}"

var _ = register(Kind{
	Name: execKind,
	Deployed: &Config{
		Doc:     "A command run on the machine that deploys, whose output is the tier's values. It runs at each deploy and on no schedule.",
		Options: ExecOptions{},
		decode:  decodeAs(decodeExec),
	},
	Dev: &Config{
		Doc:     "A command run on your machine, whose output is the values.",
		Options: ExecOptions{},
		decode:  decodeAs(decodeExec),
	},
})

type ExecOptions struct {
	Command []string `json:"command" doc:"The command to run and its arguments. {folder} in an argument is replaced with the variables folder being read."`
	Format  Format   `json:"format" enum:"json,dotenv" doc:"What the command prints: a JSON object of names to values, or KEY=VALUE lines."`
}

func (ExecOptions) Doc() string {
	return "A command whose output is a tier's values: run on the machine that deploys for production and preview, and on yours for dev."
}

func decodeExec(options ExecOptions) (behaviour, error) {
	if len(options.Command) == 0 {
		return behaviour{}, &OptionError{Field: "command", Reason: "is required: the command to run and its arguments, such as [\"op\", \"inject\"]"}
	}
	if options.Format == "" {
		return behaviour{}, &OptionError{Field: "format", Reason: "is required: one of json, dotenv"}
	}
	if err := refuseNotOneOf("format", options.Format, FormatJSON, FormatDotenv); err != nil {
		return behaviour{}, err
	}
	return behaviour{
		id: execKind,
		open: func(dir string, _ func(string) (string, bool)) (Source, error) {
			return execSource{options: options, dir: dir}, nil
		},
	}, nil
}

type execSource struct {
	options ExecOptions
	dir     string
}

func (execSource) ID() string { return execKind }

func (s execSource) Read(ctx context.Context, folders []string) (map[variablestore.Cell]Value, error) {
	out := map[variablestore.Cell]Value{}
	for _, folder := range folders {
		argv := make([]string, len(s.options.Command))
		for i, arg := range s.options.Command {
			argv[i] = strings.ReplaceAll(arg, FolderPlaceholder, folderArgument(folder))
		}
		printed, err := runCommand(ctx, s.dir, argv)
		if err != nil {
			return nil, err
		}
		parsed, err := parsePrinted(printed, s.options.Format, argv[0])
		if err != nil {
			return nil, err
		}
		for key, value := range parsed {
			if value == "" {
				continue
			}
			out[variablestore.Cell{Folder: folder, Key: key}] = Value{Plaintext: []byte(value)}
		}
	}
	return out, nil
}

func (execSource) Create(context.Context, variablestore.Cell, []byte, string) error {
	return ErrReadOnly
}

func (execSource) Update(context.Context, variablestore.Cell, []byte, string) error {
	return ErrReadOnly
}

func (execSource) URL(variablestore.Cell) string { return "" }

func folderArgument(folder string) string {
	if folder == "" {
		return rootFolderArgument
	}
	return folder
}

func runCommand(ctx context.Context, dir string, argv []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("the env source's exec names no command")
	}
	running, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(running, argv[0], argv[1:]...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &boundedBuffer{buffer: &stdout, remainingBytes: commandOutputBytes}
	cmd.Stderr = &boundedBuffer{buffer: &stderr, remainingBytes: commandStderrBytes}
	if err := cmd.Run(); err != nil {
		return nil, &commandFailure{command: argv[0], cause: err, stderr: strings.TrimSpace(stderr.String())}
	}
	return stdout.Bytes(), nil
}

type commandFailure struct {
	command string
	cause   error
	stderr  string
}

func (f *commandFailure) Error() string {
	return fmt.Sprintf("the env source's command %s failed (%v); run it yourself to see what it printed, which ocel never repeats because it may hold a value the command read", f.command, f.cause)
}

func (f *commandFailure) Unwrap() error { return f.cause }

type boundedBuffer struct {
	buffer         *bytes.Buffer
	remainingBytes int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.remainingBytes > 0 {
		take := min(len(p), b.remainingBytes)
		b.buffer.Write(p[:take])
		b.remainingBytes -= take
	}
	return len(p), nil
}

func parsePrinted(printed []byte, format Format, command string) (map[string]string, error) {
	switch format {
	case FormatDotenv:
		file, err := dotenv.Parse(bytes.NewReader(printed))
		if err != nil {
			return nil, fmt.Errorf("read what %s printed: %w", command, err)
		}
		return file.Values, nil
	case FormatJSON:
		var raw map[string]json.RawMessage
		if json.Unmarshal(printed, &raw) != nil {
			return nil, fmt.Errorf("%s printed no JSON object of names to values", command)
		}
		out := make(map[string]string, len(raw))
		for key, printedValue := range raw {
			var value string
			if json.Unmarshal(printedValue, &value) != nil {
				return nil, fmt.Errorf("%s printed %s as something other than text; quote it", command, key)
			}
			out[key] = value
		}
		return out, nil
	}
	return nil, fmt.Errorf("an env source's exec prints json or dotenv, not %q", format)
}
