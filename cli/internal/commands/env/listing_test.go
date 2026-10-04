package env

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/pkg/environment"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
	"github.com/ocelhq/ocel/pkg/variablestore"

	"github.com/ocelhq/ocel/cli/internal/clitest"
)

func TestTheListingShowsMetadataButNeverValues(t *testing.T) {
	t.Run("shows keys and metadata but never values", func(t *testing.T) {
		root := setUpEnvFixture(t).Root
		envSet(t, root, "STRIPE_API_KEY", "sk_live_secret", envOptions{})
		envSet(t, root, "POSTHOG_ID", "ph_public_id", envOptions{folder: "/web"})

		var stdout, stderr bytes.Buffer
		if err := runEnvList(context.Background(), newStreamedDependencies(&stderr), root, envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}

		out := stdout.String()
		for _, want := range []string{"STRIPE_API_KEY", "POSTHOG_ID", "/web"} {
			if !strings.Contains(out, want) {
				t.Errorf("ls stdout = %q, want it to show %q", out, want)
			}
		}
		for _, secret := range []string{"sk_live_secret", "ph_public_id"} {
			if strings.Contains(out, secret) {
				t.Errorf("ls stdout = %q, want no value printed (found %q)", out, secret)
			}
		}
	})

	t.Run("reports an empty store", func(t *testing.T) {
		root := setUpEnvFixture(t).Root

		var stdout, stderr bytes.Buffer
		if err := runEnvList(context.Background(), newStreamedDependencies(&stderr), root, envOptions{}, &stdout, &stderr); err != nil {
			t.Fatalf("runEnvList err = %v; stderr=%s", err, stderr.String())
		}
		if !strings.Contains(stdout.String(), "ocel env set") {
			t.Errorf("ls on an empty store = %q, want it to name the command that fills it", stdout.String())
		}
	})

	t.Run("a production override is orphaned though a preview shares its name", func(t *testing.T) {
		project := setUpEnvFixture(t)
		seedValue(t, project, environment.TierProduction, clitest.FixtureSlug,
			variablestore.Coordinate{Cell: variablestore.Cell{Key: "STRIPE_API_KEY"}, Environment: "staging"}, "sk_stray")
		seedEnvironment(t, project, "staging")

		var ls bytes.Buffer
		if err := runEnvList(context.Background(), newStreamedDependencies(&ls), project.Root, envOptions{}, &ls, &ls); err != nil {
			t.Fatalf("runEnvList err = %v; out=%s", err, ls.String())
		}
		if !strings.Contains(ls.String(), "orphaned") {
			t.Errorf("ls = %q, want the production row marked orphaned: no production function reads a named environment", ls.String())
		}
	})
}

func TestTheListingNamesFoldersOrphansAndDescriptions(t *testing.T) {
	t.Parallel()

	t.Run("the root folder does not print a rejected slash", func(t *testing.T) {
		t.Parallel()

		var stdout bytes.Buffer
		renderValues(&stdout, []*variablestorev1.ValueMetadata{
			{Coordinate: &variablestorev1.Coordinate{Key: "STRIPE_API_KEY", Folder: ""}},
		}, nil, nil, nil)

		out := stdout.String()
		if !strings.Contains(out, "(project root)") {
			t.Errorf("ls stdout = %q, want the root cell rendered as %q", out, "(project root)")
		}
		for _, line := range strings.Split(out, "\n") {
			for _, f := range strings.Fields(line) {
				if f == "/" {
					t.Errorf("ls stdout = %q, want no field spelled %q: --folder / is rejected", out, "/")
				}
			}
		}
	})

	t.Run("marks an override whose environment is gone", func(t *testing.T) {
		t.Parallel()

		const note = "orphaned"

		var withOrphan bytes.Buffer
		renderValues(&withOrphan, []*variablestorev1.ValueMetadata{
			{Coordinate: &variablestorev1.Coordinate{Key: "STRIPE_API_KEY"}},
			{Coordinate: &variablestorev1.Coordinate{Key: "STRIPE_API_KEY", Environment: "pr-42"}},
			{Coordinate: &variablestorev1.Coordinate{Key: "STRIPE_API_KEY", Environment: "staging"}},
		}, []string{"staging"}, nil, nil)

		out := withOrphan.String()
		if !strings.Contains(out, note) {
			t.Errorf("ls stdout = %q, want the override for pr-42 marked orphaned", out)
		}
		if !strings.Contains(out, "ocel env rm") {
			t.Errorf("ls stdout = %q, want it to name the command that removes an orphan", out)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "staging") && strings.Contains(line, note) {
				t.Errorf("ls line = %q, want an environment that still exists left unmarked", line)
			}
		}

		var live bytes.Buffer
		renderValues(&live, []*variablestorev1.ValueMetadata{
			{Coordinate: &variablestorev1.Coordinate{Key: "STRIPE_API_KEY", Environment: "staging"}},
		}, []string{"staging"}, nil, nil)
		if out := live.String(); strings.Contains(out, note) {
			t.Errorf("ls stdout = %q, want no orphan note when every override has its environment", out)
		}
	})

	t.Run("shows a declared description", func(t *testing.T) {
		t.Parallel()

		var stdout bytes.Buffer
		renderValues(&stdout, []*variablestorev1.ValueMetadata{{Coordinate: &variablestorev1.Coordinate{Key: "STRIPE_API_KEY"}}}, nil,
			[]*resourcesv1.VariableDefinition{{Key: "STRIPE_API_KEY", Description: "Used to call Stripe"}}, nil)
		if out := stdout.String(); !strings.Contains(out, "Used to call Stripe") {
			t.Errorf("ls stdout = %q, want the variable description", out)
		}
	})
}

func TestListingValuesSaysWhoItActsAsInTheCheckPhaseOfItsRunAndPrintsTheListingAloneOnStdout(t *testing.T) {
	root := setUpEnvFixture(t).Root
	envSet(t, root, "LOG_LEVEL", "debug", envOptions{})
	dependencies := newTestDependencies()
	dependencies.Presentation = func(io.Writer) terminal.Presentation {
		return terminal.Resolve(terminal.Conditions{Format: terminal.FormatJSON})
	}

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stderr)
	if err := runEnvList(context.Background(), dependencies, root, envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvList err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	evs := clitest.RunEvents(t, stderr.String())
	identity := slices.IndexFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetIdentity() != nil })
	if identity < 0 || evs[identity].GetOperation().GetPhase() != progressv1.Phase_PHASE_CHECK {
		t.Fatalf("the listing never said who it acts as in the check phase: %s", stderr.String())
	}
	if result := evs[len(evs)-1].GetSummary(); !result.GetSuccess() {
		t.Errorf("result = %v, want the listing's run to succeed", result)
	}
	if !strings.Contains(stdout.String(), "LOG_LEVEL") || strings.Contains(stderr.String(), "LOG_LEVEL") {
		t.Errorf("stdout = %q, stream = %q: want the listing on stdout and not on the stream", stdout.String(), stderr.String())
	}
}
