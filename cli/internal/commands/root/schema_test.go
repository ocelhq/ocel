package root

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/outputschema"
	"github.com/ocelhq/ocel/cli/internal/outputschema/outputschematest"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func printResultDocument(t *testing.T, result proto.Message) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := terminal.WriteResultJSON(&out, result); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestTheSchemaPrintedForEnvLsValidatesWhatEnvLsPrintsUnderJSON(t *testing.T) {
	stdout, stderr := executeRoot(t, "schema", "env", "ls")
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}
	schema := outputschematest.Compile(t, []byte(stdout))

	listed := printResultDocument(t, &resultv1.EnvListResult{
		Tier: environmentv1.Tier_TIER_PREVIEW,
		Values: []*resultv1.EnvValueSummary{
			{
				Coordinate:  &resultv1.EnvCoordinate{Project: "shop", Folder: "/web", Key: "LOG_LEVEL", Environment: "staging"},
				Description: "How much the app logs",
				Version:     4,
				Size:        5,
				UpdatedAt:   "2026-10-01T09:30:00Z",
				EnvSource:   "builtin",
			},
			{
				Coordinate: &resultv1.EnvCoordinate{Project: "shop", Key: "DATABASE_URL"},
				Target:     &resultv1.EnvCoordinate{Project: "shop", Folder: "/api", Key: "DATABASE_URL"},
			},
		},
	})
	if err := outputschematest.Validate(schema, listed); err != nil {
		t.Errorf("the schema printed for env ls rejects what env ls prints: %v\n%s", err, listed)
	}
	if err := outputschematest.Validate(schema, []byte(`{"ok":true,"data":{"values":[],"tier":"TIER_PREVIEW","unknown":1}}`)); err == nil {
		t.Error("the schema printed for env ls accepts a field no result has")
	}
}

func TestTheSchemaPrintedForADataCommandAlsoDescribesItsFailure(t *testing.T) {
	stdout, _ := executeRoot(t, "schema", "env", "ls")
	schema := outputschematest.Compile(t, []byte(stdout))

	var failure bytes.Buffer
	terminal.PrintFailureJSON(&failure, &streamv1.RunError{Code: "project.no_config", Message: "no ocel.json"})
	if err := outputschematest.Validate(schema, failure.Bytes()); err != nil {
		t.Errorf("the schema printed for env ls rejects its failure document: %v\n%s", err, failure.String())
	}
}

func TestTheSchemaPrintedForARunCommandIsTheRunEventSchema(t *testing.T) {
	stdout, _ := executeRoot(t, "schema", "deploy")
	want, err := outputschema.ReadRunEvent()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout) != strings.TrimSpace(string(want)) {
		t.Errorf("ocel schema deploy printed %.200q, want the run event schema", stdout)
	}

	schema := outputschematest.Compile(t, []byte(stdout))
	line, err := protojson.Marshal(&streamv1.RunEvent{Cli: &streamv1.RunEvent_Summary{Summary: &streamv1.RunSummary{Success: true, Headline: "deployed"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := outputschematest.Validate(schema, line); err != nil {
		t.Errorf("the schema printed for deploy rejects a run event: %v\n%s", err, line)
	}
}

func TestTheSchemaPrintedForACommandWithTwoResultsAcceptsBoth(t *testing.T) {
	stdout, _ := executeRoot(t, "schema", "domain", "ls")
	schema := outputschematest.Compile(t, []byte(stdout))

	for _, result := range []proto.Message{&resultv1.DomainListResult{}, &resultv1.PreviewDomainResult{BaseDomain: "preview.example.com"}} {
		if err := outputschematest.Validate(schema, printResultDocument(t, result)); err != nil {
			t.Errorf("the schema printed for domain ls rejects %s: %v", result.ProtoReflect().Descriptor().Name(), err)
		}
	}
}

type schemaListing struct {
	Commands []struct {
		Path   string `json:"path"`
		Output string `json:"output"`
	} `json:"commands"`
}

func TestSchemaWithoutACommandListsEveryCommandThatHasOneWithWhatItPrints(t *testing.T) {
	stdout, _ := executeRoot(t, "schema")

	outputs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		fields := strings.Fields(line)
		outputs[strings.Join(fields[:len(fields)-1], " ")] = fields[len(fields)-1]
	}
	for path, want := range map[string]string{
		"env ls":     "result",
		"doctor":     "result",
		"deploy":     "run-events",
		"preview":    "run-events",
		"preview up": "run-events",
	} {
		if outputs[path] != want {
			t.Errorf("ocel schema lists %q as %q, want %q\n%s", path, outputs[path], want, stdout)
		}
	}
	for _, path := range []string{"env set", "env", "dev"} {
		if _, listed := outputs[path]; listed {
			t.Errorf("ocel schema lists %q, a command with no schema", path)
		}
	}
}

func TestSchemaWithoutACommandUnderJSONListsThemAsOneResult(t *testing.T) {
	stdout, _ := executeRoot(t, "--json", "schema")

	var document struct {
		OK   bool          `json:"ok"`
		Data schemaListing `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil || !document.OK {
		t.Fatalf("stdout = %q, want one ok envelope: %v", stdout, err)
	}
	outputs := map[string]string{}
	for _, command := range document.Data.Commands {
		outputs[command.Path] = command.Output
	}
	for path, want := range map[string]string{
		"env ls":     "COMMAND_OUTPUT_RESULT",
		"schema":     "COMMAND_OUTPUT_RESULT",
		"deploy":     "COMMAND_OUTPUT_RUN_EVENTS",
		"preview up": "COMMAND_OUTPUT_RUN_EVENTS",
	} {
		if outputs[path] != want {
			t.Errorf("%q output = %q, want %q", path, outputs[path], want)
		}
	}
	for _, path := range []string{"env set", "env", "dev"} {
		if _, listed := outputs[path]; listed {
			t.Errorf("ocel schema lists %q, a command with no schema", path)
		}
	}

	listing := outputschematest.Compile(t, printSchemaOf(t, "schema"))
	if err := outputschematest.Validate(listing, []byte(stdout)); err != nil {
		t.Errorf("the schema printed for schema rejects what schema prints: %v", err)
	}
}

func printSchemaOf(t *testing.T, path ...string) []byte {
	t.Helper()
	stdout, _ := executeRoot(t, append([]string{"schema"}, path...)...)
	return []byte(stdout)
}

func TestSchemaOfAnUnknownCommandFailsWithUsage(t *testing.T) {
	code, stdout, stderr := executeAndReportRoot(t, "--json", "schema", "no-such-command")

	failure := requireOneFailureDocument(t, stdout)
	if failure["code"] != "usage" {
		t.Errorf("error code = %v, want usage", failure["code"])
	}
	if message, _ := failure["message"].(string); !strings.Contains(message, "no-such-command") {
		t.Errorf("error message = %q, want the unknown command named", message)
	}
	if code != 1 || stderr != "" {
		t.Errorf("exit code = %d, stderr = %q, want 1 and nothing", code, stderr)
	}
}

func TestSchemaOfACommandWithoutOneFailsWithUsageNamingTheListing(t *testing.T) {
	for _, path := range [][]string{{"env", "set"}, {"env"}, {"dev"}} {
		code, stdout, _ := executeAndReportRoot(t, append([]string{"--json", "schema"}, path...)...)

		failure := requireOneFailureDocument(t, stdout)
		if failure["code"] != "usage" {
			t.Errorf("ocel schema %s: error code = %v, want usage", strings.Join(path, " "), failure["code"])
		}
		if hint, _ := failure["hint"].(string); !strings.Contains(hint, "ocel schema") {
			t.Errorf("ocel schema %s: hint = %q, want it to point at `ocel schema`", strings.Join(path, " "), hint)
		}
		if code != 1 {
			t.Errorf("ocel schema %s: exit code = %d, want 1", strings.Join(path, " "), code)
		}
	}
}

func TestSchemaOfAHiddenCommandOrOneUnderItFailsWithUsage(t *testing.T) {
	for _, path := range [][]string{{"completion", "bash"}, {"completion"}, {"telemetry", "flush"}, {"help"}} {
		code, stdout, _ := executeAndReportRoot(t, append([]string{"--json", "schema"}, path...)...)

		failure := requireOneFailureDocument(t, stdout)
		if failure["code"] != "usage" || code != 1 {
			t.Errorf("ocel schema %s: error code = %v, exit code = %d, want usage and 1", strings.Join(path, " "), failure["code"], code)
		}
	}
}

func TestEveryCommandWhoseRunIsDrawnOnStdoutDeclaresRunEventsAndNoOtherDoes(t *testing.T) {
	ocel := newCommand()
	ocel.root.SetOut(&bytes.Buffer{})
	ocel.root.SetErr(&bytes.Buffer{})
	for _, cmd := range listLeafCommands(ocel.root) {
		drawsRunOnStdout := commands.ChooseRunOutput(cmd) == cmd.OutOrStdout()
		switch declared := commands.PrintsRunEvents(cmd); {
		case drawsRunOnStdout && !declared:
			t.Errorf("ocel %s draws its run on stdout and declares no run events, want commands.DeclareRunEvents or commands.ReserveStdout", commandPath(cmd))
		case !drawsRunOnStdout && declared:
			t.Errorf("ocel %s declares run events and its stdout is reserved, so the run is drawn on stderr", commandPath(cmd))
		}
	}
}

func TestSchemaOfAnUnknownCommandUnderHumanOutputPrintsOneFailureOnStderr(t *testing.T) {
	code, stdout, stderr := executeAndReportRoot(t, "schema", "no-such-command")

	if stdout != "" || !strings.Contains(stderr, "no-such-command") || code != 1 {
		t.Errorf("exit code = %d, stdout = %q, stderr = %q, want 1, nothing and the command named", code, stdout, stderr)
	}
}

var printsNoResultYet = []string{"env set", "env rm", "env ref", "env sync", "env ui", "logs"}

func listLeafCommands(root *cobra.Command) []*cobra.Command {
	return slices.DeleteFunc(visibleCommands(root), func(cmd *cobra.Command) bool { return cmd.HasSubCommands() })
}

func TestEveryCommandWhoseStdoutIsItsDataDeclaresItsResultsUnlessItPrintsNone(t *testing.T) {
	for _, cmd := range listLeafCommands(newCommand().root) {
		path := commandPath(cmd)
		_, declared := commands.FindResults(cmd)
		switch {
		case !commands.PrintsData(cmd):
		case declared && slices.Contains(printsNoResultYet, path):
			t.Errorf("ocel %s declares a result, so it leaves printsNoResultYet", path)
		case !declared && !slices.Contains(printsNoResultYet, path):
			t.Errorf("ocel %s prints data and declares no result, want commands.DeclareResult", path)
		}
	}
}

func TestEveryDeclaredResultHasAPublishedSchema(t *testing.T) {
	for _, cmd := range listLeafCommands(newCommand().root) {
		results, declared := commands.FindResults(cmd)
		if !declared {
			continue
		}
		schema, err := outputschema.ComposeResult(results...)
		if err != nil {
			t.Errorf("ocel %s: %v", commandPath(cmd), err)
			continue
		}
		outputschematest.Compile(t, schema)
	}
}
