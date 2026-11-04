package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/jobcontext"
	"github.com/ocelhq/ocel/cli/internal/jobinput"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/environment"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

const stripeSecretRequired = `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SECRET","required":true,"description":"Charges cards."}]`

type ocelCloudAnswers struct {
	t      *testing.T
	out    *syncBuffer
	stdin  *io.PipeWriter
	inputs []*runnerv1.JobInput
}

func inAJob(t *testing.T, dependencies *Dependencies) *ocelCloudAnswers {
	t.Helper()
	stdin, write := io.Pipe()
	t.Cleanup(func() { _ = write.Close() })
	dependencies.JobContext = &jobcontext.Context{Job: &jobcontext.Job{ID: "job_1", Slug: clitest.FixtureSlug, Input: jobinput.New(stdin)}}
	useJSONFormat(t, dependencies)
	answers := &ocelCloudAnswers{t: t, out: &syncBuffer{}, stdin: write}
	dependencies.Events.Attach(terminal.NewJSONLines(answers.out))
	return answers
}

func (a *ocelCloudAnswers) next() *runnerv1.JobInput {
	a.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		var held []*runnerv1.JobInput
		for _, ev := range envelopes(a.t, a.out.String()) {
			if input := ev.GetWaiting().GetInput(); input != nil {
				held = append(held, input)
			}
		}
		if len(held) > len(a.inputs) {
			a.inputs = append(a.inputs, held[len(a.inputs)])
			return a.inputs[len(a.inputs)-1]
		}
		time.Sleep(20 * time.Millisecond)
	}
	a.t.Fatalf("the run asked Ocel Cloud for no input %d: %s", len(a.inputs)+1, a.out.String())
	return nil
}

func (a *ocelCloudAnswers) answer(line string) {
	a.t.Helper()
	if _, err := io.WriteString(a.stdin, line+"\n"); err != nil {
		a.t.Fatalf("answer on stdin: %v", err)
	}
}

func deployingInAJob(t *testing.T, dependencies Dependencies, root string, opts deployOptions) chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- runDeploy(context.Background(), dependencies, root, opts, io.Discard, io.Discard, strings.NewReader(""))
	}()
	return done
}

func ended(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(60 * time.Second):
		t.Fatal("the deploy never returned once Ocel Cloud answered")
		return nil
	}
}

func TestInsideAJobMissingVariablesAreAskedOfOcelCloudAndReadAgainFromTheStoreOnceResolved(t *testing.T) {
	fixture := setUpVariablesProject(t, stripeSecretRequired)
	dependencies := newTestDependencies()
	built := false
	stubAppBuildRecorder(&dependencies, &built)
	cloud := inAJob(t, &dependencies)

	done := deployingInAJob(t, dependencies, fixture.Root, deployOptions{})

	first := cloud.next()
	needed := first.GetVariables()
	if needed.GetTier() != environmentv1.Tier_TIER_PRODUCTION || len(needed.GetVariables()) != 1 {
		t.Fatalf("asked for %v, want the production values the deploy misses", needed)
	}
	if v := needed.GetVariables()[0]; v.GetKey() != "STRIPE_API_KEY" || v.GetFolder() != "" || !v.GetSecret() || v.GetDescription() != "Charges cards." {
		t.Errorf("asked for %v, want STRIPE_API_KEY at the root, as a secret, with its description", v)
	}
	cloud.answer(fmt.Sprintf(`{"inputId":%q,"resolved":true}`, first.GetId()))

	second := cloud.next()
	if second.GetId() == first.GetId() || second.GetVariables().GetVariables()[0].GetKey() != "STRIPE_API_KEY" {
		t.Fatalf("asked again %v, want STRIPE_API_KEY asked for again under a new id while the store holds no value", second)
	}
	setValue(t, fixture, variablestore.Scope{Project: clitest.FixtureSlug, Tier: environment.TierProduction}, "STRIPE_API_KEY", "sk_live_filled_in", envOptions{})
	cloud.answer(fmt.Sprintf(`{"inputId":%q,"resolved":true}`, second.GetId()))

	if err := ended(t, done); err != nil {
		t.Fatalf("deploy err = %v, want it to go on with the value Ocel Cloud wrote: %s", err, cloud.out.String())
	}
	if !built {
		t.Error("the apps were never built once the values were resolved")
	}
	resumed := slices.ContainsFunc(envelopes(t, cloud.out.String()), func(ev *streamv1.RunEvent) bool { return ev.GetResumed() != nil })
	if !resumed {
		t.Errorf("the run was never resumed: %s", cloud.out.String())
	}
}

func TestInsideAJobVariablesLeftUnresolvedOnOcelCloudFailTheDeployAsMissing(t *testing.T) {
	fixture := setUpVariablesProject(t, stripeSecretRequired)
	dependencies := newTestDependencies()
	cloud := inAJob(t, &dependencies)

	done := deployingInAJob(t, dependencies, fixture.Root, deployOptions{})
	cloud.answer(fmt.Sprintf(`{"inputId":%q,"resolved":false}`, cloud.next().GetId()))

	err := ended(t, done)
	if got := clierror.NewRunError(err); got.GetCode() != clierror.CodeVariablesMissing || !strings.Contains(got.GetMessage(), "The console") {
		t.Errorf("run error = %v, want %s saying the console gave no values", got, clierror.CodeVariablesMissing)
	}
	if message := err.Error(); !strings.Contains(message, "Fill them in: run `ocel deploy` again and enter them on its job page") || strings.Contains(message, "ocel env") {
		t.Errorf("err = %v, want it to send the values to the job page of a deploy run again, not to ocel env", err)
	}
}

func TestInsideAJobYesIsRefusedBecauseOcelCloudAnswersEveryConfirmation(t *testing.T) {
	fixture := setUpDeployProject(t)
	dependencies := newTestDependencies()
	inAJob(t, &dependencies)

	err := ended(t, deployingInAJob(t, dependencies, fixture.Root, deployOptions{yes: true}))
	var coded *clierror.Error
	if !errors.As(err, &coded) || coded.Code != clierror.CodeUsage || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("err = %v, want --yes refused as usage inside a job", err)
	}
}

func TestInsideAJobMissingValuesWithNoVariablesKeyRecordTheWantForTheNextBootstrap(t *testing.T) {
	fixture := setUpVariablesProject(t, stripeSecretRequired)
	offerVariablesKey(fixture)
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	cloud := inAJob(t, &dependencies)

	err := ended(t, deployingInAJob(t, dependencies, fixture.Root, deployOptions{}))
	if err == nil || !strings.Contains(err.Error(), "ocel bootstrap production") {
		t.Fatalf("deploy err = %v, want the missing key refused naming the bootstrap to run: %s", err, cloud.out.String())
	}
	wants, err := stackrecords.ListBootstrapWants(context.Background(), fixture.Provider.KeyValues(), environment.TierProduction)
	if err != nil {
		t.Fatal(err)
	}
	if len(wants[provider.VariablesKeyPart]) != 1 {
		t.Fatalf("wants = %v, want the job to record the key as wanted, for `ocel bootstrap production` to add", wants)
	}
}
