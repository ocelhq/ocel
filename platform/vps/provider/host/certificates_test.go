package host

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func TestAnEngineThatCannotBeReachedIsReportedRatherThanReadAsNoTrouble(t *testing.T) {
	t.Parallel()

	box := machine(nil)
	box.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, dockerReach) {
			return session.Result{Code: 1, Stderr: "Cannot connect to the Docker daemon"}, true
		}
		return session.Result{}, false
	}
	_, err := box.host().FrontProxy().Certificate(context.Background(), "shop.example.com")
	if err == nil {
		t.Fatal("Certificate() read an engine it could not reach as a proxy with nothing to say")
	}
	if !strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
		t.Errorf("Certificate() = %v, want the engine's own refusal passed out", err)
	}
}
