package boxstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

type LocalTransport struct {
	Elevation []string
}

func (LocalTransport) HasStore(_ context.Context, tier environment.Tier) (bool, error) {
	if _, err := os.Stat(RecordsHelper); err != nil {
		return false, nil
	}
	info, err := os.Stat(live.RecordsDir(live.StateRoot, tier))
	return err == nil && info.IsDir(), nil
}

func (LocalTransport) Records(ctx context.Context, tier environment.Tier, stdin io.Reader, argv ...string) (string, error) {
	stdout, stderr, code, err := runCommand(ctx, stdin, RecordsHelper, append([]string{string(tier)}, argv...)...)
	switch {
	case err != nil:
		return "", refusal.Refuse(refusal.CodeDenied, "run the records helper on this host: %s", err)
	case code == 0:
		return stdout, nil
	case code == ExitNoRecord:
		return "", records.ErrNotFound
	case code == ExitStale:
		return "", records.ErrStale
	default:
		return "", refusal.Refuse(refusal.CodeDenied, "records %s on this host: %s", argv[0], describeFailure(stderr, code))
	}
}

func (l LocalTransport) Seal(ctx context.Context, what string, argv []string, stdin io.Reader) (string, error) {
	elevated := append(append([]string{}, l.Elevation...), argv...)
	stdout, stderr, code, err := runCommand(ctx, stdin, elevated[0], elevated[1:]...)
	switch {
	case err != nil:
		return "", refusal.Refuse(refusal.CodeDenied, "run the seal helper on this host: %s", err)
	case code == 0:
		return stdout, nil
	default:
		return "", refusal.Refuse(refusal.CodeDenied, "%s on this host: %s", what, describeFailure(stderr, code))
	}
}

func runCommand(ctx context.Context, stdin io.Reader, name string, argv ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, name, argv...)
	cmd.Stdin = stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exited *exec.ExitError
	switch {
	case err == nil:
		return stdout.String(), stderr.String(), 0, nil
	case errors.As(err, &exited):
		return stdout.String(), stderr.String(), exited.ExitCode(), nil
	default:
		return "", "", 0, err
	}
}

func describeFailure(stderr string, code int) string {
	said := strings.TrimSpace(stderr)
	if said == "" {
		return "it exited " + strconv.Itoa(code)
	}
	return said
}

var (
	_ RecordTransport = LocalTransport{}
	_ SealTransport   = LocalTransport{}
)
