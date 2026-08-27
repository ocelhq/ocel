package providerkit_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
)

func appBarrier(t *testing.T, width int) func(providerkit.StackPlan) error {
	t.Helper()
	var mu sync.Mutex
	var arrived int
	gate := make(chan struct{})
	return func(plan providerkit.StackPlan) error {
		if plan.App == nil {
			return nil
		}
		mu.Lock()
		arrived++
		full := arrived == width
		mu.Unlock()
		if full {
			close(gate)
		}
		select {
		case <-gate:
			return nil
		case <-time.After(5 * time.Second):
			return fmt.Errorf("%s waited for %d apps to be provisioning at once and only %d ever were", plan.App.App, width, arrived)
		}
	}
}

func TestDeployProvisionsAppsAtTheSameTime(t *testing.T) {
	builtProject(t)
	client, provider := deployServed(t)
	provider.Releases().(*fake.Releaser).Entering(appBarrier(t, 2))

	result, _ := deploy(t, client, twoAppRequest())
	if result == nil || !result.GetSuccess() {
		t.Fatalf("Deploy() = %q, want two apps standing up at once and the deploy succeeding", result.GetError())
	}
}
