package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/cli/clitest"
	"github.com/ocelhq/ocel/cli/internal/cli/cmddeps"
	"github.com/ocelhq/ocel/cli/internal/runui"
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

func jsonRecords(t *testing.T, out string) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		records = append(records, rec)
	}
	return records
}

func recordsOfType(records []map[string]any, kind string) []map[string]any {
	var out []map[string]any
	for _, rec := range records {
		if rec["type"] == kind {
			out = append(out, rec)
		}
	}
	return out
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

	failed := recordsOfType(jsonRecords(t, stdout.String()), "failed")
	if len(failed) != 1 {
		t.Fatalf("got %d failed records, want 1: %s", len(failed), stdout.String())
	}
	message, _ := failed[0]["error"].(string)
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

	degraded := recordsOfType(jsonRecords(t, stdout.String()), "degraded")
	if len(degraded) != 2 {
		t.Fatalf("got %d degraded records, want one per waived need: %s", len(degraded), stdout.String())
	}
	if degraded[0]["need"] != "edge-middleware" || degraded[1]["need"] != "ppr-resume" {
		t.Errorf("degraded records = %v, want edge-middleware then ppr-resume", degraded)
	}
	detail, ok := degraded[0]["detail"].(string)
	if !ok || !strings.Contains(detail, "next start") {
		t.Errorf("degraded detail = %v, want the degrade spelled out", degraded[0]["detail"])
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
		if got := recordsOfType(jsonRecords(t, stdout.String()), "degraded"); len(got) != 0 {
			t.Errorf("got %d degraded records, want none: %v", len(got), got)
		}
	})
}
