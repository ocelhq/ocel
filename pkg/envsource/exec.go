package envsource

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

	"github.com/ocelhq/ocel/pkg/dotenv"
	"github.com/ocelhq/ocel/pkg/envvars"
)

const (
	commandTimeout      = 2 * time.Minute
	commandOutputBytes  = 1 << 20
	commandStderrBytes  = 2048
	rootFolderArgument  = "/"
	contentVersionBytes = 8
)

type execSource struct {
	options ExecOptions
	dir     string
}

func (execSource) ID() string { return string(Exec) }

func (s execSource) Read(ctx context.Context, folders []string) (map[envvars.Cell]Value, error) {
	out := map[envvars.Cell]Value{}
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
			out[envvars.Cell{Folder: folder, Key: key}] = Value{Plaintext: []byte(value), Version: contentVersion(value)}
		}
	}
	return out, nil
}

func (execSource) Create(context.Context, envvars.Cell, []byte, string) error { return ErrReadOnly }

func (execSource) URL(envvars.Cell) string { return "" }

func folderArgument(folder string) string {
	if folder == "" {
		return rootFolderArgument
	}
	return folder
}

func contentVersion(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:contentVersionBytes])
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
		said := strings.TrimSpace(stderr.String())
		if said != "" {
			said = ": " + said
		}
		return nil, fmt.Errorf("the env source's command %s failed (%w)%s", argv[0], err, said)
	}
	return stdout.Bytes(), nil
}

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
