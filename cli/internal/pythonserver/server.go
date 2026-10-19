package pythonserver

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ocelhq/ocel/cli/internal/childprocess"
	"github.com/ocelhq/ocel/pkg/containerimage"
)

const (
	File = "ocel-server.py"

	definesAppFlag = "--defines-app"
	definesAppYes  = "yes"
	definesAppNo   = "no"
)

//go:embed ocel-server.py
var Source string

func (e Entrypoint) Command() []string {
	return append([]string{containerimage.PythonWorkerEntry.Interpreter, File}, e.arguments()...)
}

func (e Entrypoint) DefinesApp(ctx context.Context, interpreter, dir string) (bool, error) {
	cmd := exec.Command(interpreter, append([]string{"-I", File, definesAppFlag}, e.arguments()...)...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := childprocess.Run(ctx, cmd); err != nil {
		return false, fmt.Errorf("read whether %s defines app or application, which the server program serves (%w):\n%s", e, err, stderr.String())
	}
	switch said := strings.TrimSpace(stdout.String()); said {
	case definesAppYes:
		return true, nil
	case definesAppNo:
		return false, nil
	default:
		return false, fmt.Errorf("read whether %s defines app or application: the server program said %q", e, said)
	}
}
