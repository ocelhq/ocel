package host

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func TestAnEngineThatCannotBeReachedIsReportedRatherThanReadAsNoTrouble(t *testing.T) {
	t.Parallel()

	box := newBench(nil)
	box.answer = func(command string) (ssh.Result, bool) {
		if strings.Contains(command, ssh.DockerReach) {
			return ssh.Result{Code: 1, Stderr: "Cannot connect to the Docker daemon"}, true
		}
		return ssh.Result{}, false
	}
	_, err := box.host().FrontProxy().Certificate(context.Background(), "shop.example.com")
	if err == nil {
		t.Fatal("Certificate() read an engine it could not reach as a proxy with nothing to say")
	}
	if !strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
		t.Errorf("Certificate() = %v, want the engine's own refusal passed out", err)
	}
}
