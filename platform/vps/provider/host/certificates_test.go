package host

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func TestAnEngineThatCannotBeReachedIsReportedRatherThanReadAsNoTrouble(t *testing.T) {
	t.Parallel()

	stand := machine(nil)
	stand.answer = func(command string) (session.Result, bool) {
		if strings.Contains(command, dockerReach) {
			return session.Result{Code: 1, Stderr: "Cannot connect to the Docker daemon"}, true
		}
		return session.Result{}, false
	}
	err := stand.host().CertificateTrouble(context.Background(), "shop.example.com")
	if err == nil {
		t.Fatal("CertificateTrouble() read an engine it could not reach as a proxy with nothing to say")
	}
	if !strings.Contains(err.Error(), "Cannot connect to the Docker daemon") {
		t.Errorf("CertificateTrouble() = %v, want the engine's own refusal carried out", err)
	}
}
