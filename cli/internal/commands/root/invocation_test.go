package root

import (
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/run"
)

func TestEveryCommandReportsDeploymentsWithTheSavedConsoleLogin(t *testing.T) {
	invocation := newInvocation(run.NewBus(time.Now), &flags{})

	if invocation.DeploymentReports.LoadCredentials == nil {
		t.Fatal("DeploymentReports loads no credentials, so every deployment would be reported as from a user who is not logged in")
	}
}
