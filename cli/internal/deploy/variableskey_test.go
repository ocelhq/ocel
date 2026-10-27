package deploy

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var variablesKey = provider.Part{Name: provider.VariablesKeyPart, Summary: "a key the variables are sealed under"}

func offerVariablesKey(fixture clitest.FakeProject) {
	fixture.Provider.FakeBootstrap().Offers(append(fixture.Provider.FakeBootstrap().Parts(), variablesKey)...)
}

func appliedVariablesKey(fixture clitest.FakeProject) bool {
	for _, applied := range fixture.Provider.FakeBootstrap().Applied() {
		if applied.Tier == environment.TierProduction && slices.Contains(applied.Parts, provider.VariablesKeyPart) {
			return true
		}
	}
	return false
}

type terminalDeploy struct {
	out    syncBuffer
	stderr bytes.Buffer
	opened []string
	mu     sync.Mutex
	done   chan error
}

func startTerminalDeploy(t *testing.T, fixture clitest.FakeProject, stdin io.Reader) *terminalDeploy {
	t.Helper()
	dependencies := newTestDependencies()
	terminalStdin(&dependencies)
	withSetups(t, &dependencies)
	run := &terminalDeploy{done: make(chan error, 1)}
	recordBrowser(&dependencies, &run.opened, &run.mu)
	stubBuild(&dependencies, nil)
	go func() {
		clitest.AttachTerminalSink(dependencies.Invocation, &run.out)
		run.done <- runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{}, &run.out, &run.stderr, stdin)
	}()
	return run
}

func (r *terminalDeploy) wait(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.done:
		return err
	case <-time.After(60 * time.Second):
		t.Fatalf("runDeploy never returned; stdout=%s", r.out.String())
		return nil
	}
}

func (r *terminalDeploy) openedPages() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.opened)
}

func TestADeployMissingValuesOcelStoresAddsTheVariablesKeyBeforeOpeningThePage(t *testing.T) {
	t.Run("an accepted offer bootstraps the key, then the page opens and the deploy resumes", func(t *testing.T) {
		fixture := setUpVariablesProject(t, stripeSecretRequired)
		offerVariablesKey(fixture)
		problems := problemsFile(t, missingStripeKey)

		run := startTerminalDeploy(t, fixture, answering(answer{line: "y"}, answer{line: "y"}))
		address, token := awaitEditorURL(t, &run.out, 1)
		if !appliedVariablesKey(fixture) {
			t.Errorf("the page opened before %s was bootstrapped, so nothing on it could be saved", provider.VariablesKeyPart)
		}
		setCell(t, address, token, "STRIPE_API_KEY", "sk_live_filled_in")
		clitest.WriteFile(t, problems, "[]")
		markDone(t, address, token)

		if err := run.wait(t); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, run.out.String(), run.stderr.String())
		}
		if !strings.Contains(run.out.String(), "Deployed") {
			t.Errorf("stdout = %q, want the deploy to finish once the value is set", run.out.String())
		}
	})

	t.Run("a declined offer opens no page, records the want and names the bootstrap to run", func(t *testing.T) {
		fixture := setUpVariablesProject(t, stripeSecretRequired)
		offerVariablesKey(fixture)
		problemsFile(t, missingStripeKey)

		run := startTerminalDeploy(t, fixture, answering(answer{line: "n"}))
		if err := run.wait(t); err != nil {
			t.Fatalf("runDeploy err = %v, want a declined setup to end the run as every declined setup does; stdout=%s", err, run.out.String())
		}
		if sent := sentDeploys(t, fixture); len(sent) != 0 {
			t.Errorf("the CLI sent %d deploys, want none", len(sent))
		}
		if opened := run.openedPages(); len(opened) != 0 {
			t.Errorf("opened = %v, want no page when nothing on it could be saved", opened)
		}
		if want := "`ocel bootstrap production`"; !strings.Contains(run.out.String(), want) {
			t.Errorf("stdout = %q, want it to name %s", run.out.String(), want)
		}
		wants, err := stackrecords.ListBootstrapWants(context.Background(), fixture.Provider.KeyValues(), environment.TierProduction)
		if err != nil {
			t.Fatal(err)
		}
		if len(wants[provider.VariablesKeyPart]) != 1 {
			t.Errorf("wants = %v, want this project's want of %s, for `ocel bootstrap production` to add", wants, provider.VariablesKeyPart)
		}
	})

	t.Run("an installed key opens the page without offering a bootstrap", func(t *testing.T) {
		fixture := setUpVariablesProject(t, stripeSecretRequired)
		offerVariablesKey(fixture)
		clitest.Bootstrap(t, fixture.Provider, environment.TierProduction, fake.PartCache, fake.PartImages, provider.VariablesKeyPart)
		bootstrapped := len(fixture.Provider.FakeBootstrap().Applied())
		problems := problemsFile(t, missingStripeKey)

		run := startTerminalDeploy(t, fixture, strings.NewReader(""))
		address, token := awaitEditorURL(t, &run.out, 1)
		setCell(t, address, token, "STRIPE_API_KEY", "sk_live_filled_in")
		clitest.WriteFile(t, problems, "[]")
		markDone(t, address, token)

		if err := run.wait(t); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s", err, run.out.String())
		}
		if applied := fixture.Provider.FakeBootstrap().Applied(); len(applied) != bootstrapped {
			t.Errorf("the deploy applied %d bootstraps, want none when the key is installed", len(applied)-bootstrapped)
		}
	})
}
