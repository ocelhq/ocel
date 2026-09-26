package hostports

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

type Records struct{}

func (Records) HasStore(_ context.Context, class edge.Class) (bool, error) {
	if _, err := os.Stat(host.RecordsHelper); err != nil {
		return false, nil
	}
	info, err := os.Stat(host.RecordsDir(class))
	return err == nil && info.IsDir(), nil
}

func (Records) Records(ctx context.Context, class edge.Class, stdin io.Reader, argv ...string) (string, error) {
	stdout, stderr, code, err := ran(ctx, stdin, host.RecordsHelper, append([]string{string(class)}, argv...)...)
	switch {
	case err != nil:
		return "", refusal.Refuse(refusal.CodeDenied, "run the records helper on this host: %s", err)
	case code == 0:
		return stdout, nil
	case code == host.ExitNoRecord:
		return "", records.ErrNotFound
	case code == host.ExitStale:
		return "", records.ErrStale
	default:
		return "", refusal.Refuse(refusal.CodeDenied, "records %s on this host: %s", argv[0], terse(stderr, code))
	}
}

type Cipher struct{}

func (Cipher) Seal(ctx context.Context, what string, argv []string, stdin io.Reader) (string, error) {
	stdout, stderr, code, err := ran(ctx, stdin, "sudo", append([]string{"-n"}, argv...)...)
	switch {
	case err != nil:
		return "", refusal.Refuse(refusal.CodeDenied, "run the seal helper on this host: %s", err)
	case code == 0:
		return stdout, nil
	default:
		return "", refusal.Refuse(refusal.CodeDenied, "%s on this host: %s", what, terse(stderr, code))
	}
}

func ran(ctx context.Context, stdin io.Reader, name string, argv ...string) (string, string, int, error) {
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

func terse(stderr string, code int) string {
	said := strings.TrimSpace(stderr)
	if said == "" {
		return "it exited " + strconv.Itoa(code)
	}
	return said
}

var (
	_ host.RecordTransport = Records{}
	_ host.SealTransport   = Cipher{}
)
