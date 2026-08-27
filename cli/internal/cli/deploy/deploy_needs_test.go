package deploy

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/runui"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const needsRefusal = "app web needs edge-middleware and the \"cloudfront\" edge does not serve it: middleware runs in the origin's Node server the way `next start` runs it, so every request pays the round trip to the origin before it is routed. " +
	"It affects routes /dashboard, /admin. " +
	"Add \"edge-middleware\" to `allowDegraded` in ocel.config.ts to deploy it degraded, or move the app to an edge that serves edge-middleware"

const degradedDetail = "web: middleware runs in the origin's Node server the way `next start` runs it, so every request pays the round trip to the origin before it is routed. It affects routes /dashboard"

func useJSONLogFormat(t *testing.T, deps *cmddeps.Deps) {
	t.Helper()
	deps.Presentation = func(io.Writer) runui.Presentation {
		return runui.Resolve(runui.Origin{LogFormat: runui.FormatJSON})
	}
}

func envelopes(t *testing.T, out string) []*streamv1.RunEvent {
	t.Helper()
	var events []*streamv1.RunEvent
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		ev := &streamv1.RunEvent{}
		if err := protojson.Unmarshal([]byte(line), ev); err != nil {
			t.Fatalf("line %q is not a protojson RunEvent: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

func TestDeployRendersTheNeedsRefusalInHumanMode(t *testing.T) {
	root, _, deps := clitest.SetUpEdgeFixture(t, "")
	t.Setenv(clitest.FakeNeedsRefusalEnvVar, needsRefusal)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want the unsupported need to fail the deploy; stdout=%s stderr=%s", stdout.String(), stderr.String())
	}

	rendered := stdout.String() + stderr.String()
	for _, want := range []string{"edge-middleware", "next start", "/dashboard", "/admin", `"edge-middleware" to ` + "`allowDegraded`"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered output = %q, want it to carry %q", rendered, want)
		}
	}
}

func TestDeployRendersTheNeedsRefusalInJSONMode(t *testing.T) {
	root, _, deps := clitest.SetUpEdgeFixture(t, "")
	useJSONLogFormat(t, &deps)
	t.Setenv(clitest.FakeNeedsRefusalEnvVar, needsRefusal)

	var stdout, stderr bytes.Buffer
	err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
	if err == nil {
		t.Fatalf("runDeploy err = nil, want the unsupported need to fail the deploy; stdout=%s", stdout.String())
	}

	var message string
	for _, ev := range envelopes(t, stdout.String()) {
		if res := ev.GetResult(); res != nil {
			if res.GetSuccess() {
				t.Fatalf("run result reports success, want the unsupported need to fail the run: %s", stdout.String())
			}
			message = res.GetError()
		}
	}
	if message == "" {
		t.Fatalf("no failing run result on the stream: %s", stdout.String())
	}
	for _, want := range []string{"edge-middleware", "next start", "/dashboard", `"edge-middleware" to ` + "`allowDegraded`"} {
		if !strings.Contains(message, want) {
			t.Errorf("failed record error = %q, want it to carry %q", message, want)
		}
	}
}

func TestDeployRendersADegradedNeedInHumanMode(t *testing.T) {
	root, _, deps := clitest.SetUpEdgeFixture(t, "  allowDegraded: [\"edge-middleware\"],\n")
	t.Setenv(clitest.FakeDegradedEnvVar, "edge-middleware="+degradedDetail)

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	rendered := stdout.String() + stderr.String()
	for _, want := range []string{"edge-middleware", "next start", "/dashboard"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered output = %q, want the need named and the degrade spelled out (%q)", rendered, want)
		}
	}
}

func TestDeployRendersADegradedNeedAsATypedJSONRecord(t *testing.T) {
	root, _, deps := clitest.SetUpEdgeFixture(t, "  allowDegraded: [\"edge-middleware\", \"ppr-resume\"],\n")
	useJSONLogFormat(t, &deps)
	t.Setenv(clitest.FakeDegradedEnvVar, "edge-middleware="+degradedDetail+";ppr-resume=web: the shell comes from the origin. It affects routes /")

	var stdout, stderr bytes.Buffer
	if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}

	var degraded []*progressv1.DegradedEvent
	for _, ev := range envelopes(t, stdout.String()) {
		if d := ev.GetOperation().GetDegraded(); d != nil {
			degraded = append(degraded, d)
		}
	}
	if len(degraded) != 2 {
		t.Fatalf("got %d degraded envelopes, want one per waived need: %s", len(degraded), stdout.String())
	}
	if degraded[0].GetNeed() != "edge-middleware" || degraded[1].GetNeed() != "ppr-resume" {
		t.Errorf("degraded envelopes = %v, want edge-middleware then ppr-resume", degraded)
	}
	if !strings.Contains(degraded[0].GetDetail(), "next start") {
		t.Errorf("degraded detail = %q, want the degrade spelled out", degraded[0].GetDetail())
	}
}

func TestDeploySaysNothingAboutNeedsForAnAppThatDeclaresNone(t *testing.T) {
	t.Run("human", func(t *testing.T) {
		root, _, deps := clitest.SetUpEdgeFixture(t, "")

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		rendered := stdout.String() + stderr.String()
		for _, unwanted := range []string{"next start", "allowDegraded", "degraded"} {
			if strings.Contains(rendered, unwanted) {
				t.Errorf("rendered output = %q, want no needs notice for an app that declares none (%q)", rendered, unwanted)
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		root, _, deps := clitest.SetUpEdgeFixture(t, "")
		useJSONLogFormat(t, &deps)

		var stdout, stderr bytes.Buffer
		if err := runDeploy(context.Background(), deps, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
		}
		for _, ev := range envelopes(t, stdout.String()) {
			if d := ev.GetOperation().GetDegraded(); d != nil {
				t.Errorf("degraded envelope %v on the stream, want none for an app that declares no needs", d)
			}
		}
	})
}
