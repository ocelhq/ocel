package outputschema_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/outputschema"
	"github.com/ocelhq/ocel/cli/internal/outputschema/outputschematest"
	"github.com/ocelhq/ocel/cli/internal/run"
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

func buildEnvListSchema(t *testing.T) []byte {
	t.Helper()
	schema, err := outputschema.Result(new(resultv1.EnvListResult).ProtoReflect().Descriptor().FullName())
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func TestTheResultSchemaAcceptsTheResultEnvelopeAProgramPrints(t *testing.T) {
	compiled := outputschematest.Compile(t, buildEnvListSchema(t))

	document := printResultDocument(t, &resultv1.EnvListResult{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
		Values: []*resultv1.EnvValueSummary{{
			Coordinate: &resultv1.EnvCoordinate{Project: "shop", Folder: "/web", Key: "LOG_LEVEL"},
			Version:    3,
			Size:       5,
			UpdatedAt:  "2026-10-01T09:30:00Z",
		}},
	})
	if err := outputschematest.Validate(compiled, document); err != nil {
		t.Fatalf("the schema rejects %s: %v", document, err)
	}
}

func TestTheResultSchemaAcceptsTheFailureEnvelope(t *testing.T) {
	compiled := outputschematest.Compile(t, buildEnvListSchema(t))

	var out bytes.Buffer
	terminal.PrintFailureJSON(&out, &streamv1.RunError{Code: "project.no_config", Message: "no ocel.json", Hint: proto.String("run `ocel init`")})
	if err := outputschematest.Validate(compiled, out.Bytes()); err != nil {
		t.Fatalf("the schema rejects %s: %v", out.String(), err)
	}
}

func TestTheResultSchemaRejectsAMisspelledFieldAndAMalformedEnvelope(t *testing.T) {
	compiled := outputschematest.Compile(t, buildEnvListSchema(t))

	for name, document := range map[string]string{
		"a misspelled field":      `{"ok":true,"data":{"tier":"TIER_PRODUCTION","valuez":[]}}`,
		"another result":          string(printResultDocument(t, &resultv1.LogoutResult{LoggedOut: true})),
		"no ok flag":              `{"data":{"values":[]}}`,
		"success carrying error":  `{"ok":true,"error":{"code":"internal","message":"x","retryable":false}}`,
		"a failure carrying data": `{"ok":false,"data":{"values":[]}}`,
	} {
		if err := outputschematest.Validate(compiled, []byte(document)); err == nil {
			t.Errorf("the schema accepts %s: %s", name, document)
		}
	}
}

func TestTheResultSchemaOfSeveralResultsAcceptsAnyOfThemAndNothingElse(t *testing.T) {
	schema, err := outputschema.Result(
		new(resultv1.DomainListResult).ProtoReflect().Descriptor().FullName(),
		new(resultv1.PreviewDomainResult).ProtoReflect().Descriptor().FullName(),
	)
	if err != nil {
		t.Fatal(err)
	}
	compiled := outputschematest.Compile(t, schema)

	for _, result := range []proto.Message{&resultv1.DomainListResult{}, &resultv1.PreviewDomainResult{BaseDomain: "preview.example.com"}} {
		document := printResultDocument(t, result)
		if err := outputschematest.Validate(compiled, document); err != nil {
			t.Errorf("the schema rejects %s: %v", document, err)
		}
	}
	if err := outputschematest.Validate(compiled, printResultDocument(t, &resultv1.LogoutResult{})); err == nil {
		t.Error("the schema accepts the result of a command it was not built for")
	}
}

func TestTheResultSchemaOfAnUnknownMessageIsRefused(t *testing.T) {
	if _, err := outputschema.Result("cli.result.v1.NoSuchResult"); err == nil || !strings.Contains(err.Error(), "cli.result.v1.NoSuchResult") {
		t.Fatalf("err = %v, want one naming the message", err)
	}
}

func TestTheResultSchemaOfNoMessageIsRefused(t *testing.T) {
	if _, err := outputschema.Result(); err == nil {
		t.Fatal("err = nil, want the empty list of results refused")
	}
}

func TestTheRunEventSchemaAcceptsARunEventAndRejectsAMisspelledField(t *testing.T) {
	schema, err := outputschema.RunEvent()
	if err != nil {
		t.Fatal(err)
	}
	compiled := outputschematest.Compile(t, schema)

	event, err := protojson.Marshal(&streamv1.RunEvent{Cli: &streamv1.RunEvent_Summary{Summary: &streamv1.RunSummary{Headline: "done"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := outputschematest.Validate(compiled, event); err != nil {
		t.Fatalf("the schema rejects %s: %v", event, err)
	}
	if err := outputschematest.Validate(compiled, []byte(`{"summary":{"headlin":"done"}}`)); err == nil {
		t.Error("the schema accepts a misspelled field")
	}
}

func TestTheRunEventSchemaAcceptsEveryLineTheNDJSONStreamPrints(t *testing.T) {
	compiled := func() *jsonschema.Schema {
		schema, err := outputschema.RunEvent()
		if err != nil {
			t.Fatal(err)
		}
		return outputschematest.Compile(t, schema)
	}()

	var out bytes.Buffer
	bus := run.NewBus(time.Now)
	bus.Attach(terminal.NewJSONLines(&out))
	_, current, err := bus.Begin(context.Background(), "ocel deploy", "")
	if err != nil {
		t.Fatal(err)
	}
	current.Hold(&streamv1.WaitingEvent{})("the page was answered")
	var failure error = &clierror.Error{Code: clierror.CodeProjectNoConfig, Hint: "run `ocel init`", Cause: errors.New("no ocel.json")}
	current.End(&failure)
	if err := bus.Close(); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("the stream printed %d lines, want a waiting event and a summary", len(lines))
	}
	for _, line := range lines {
		if err := outputschematest.Validate(compiled, []byte(line)); err != nil {
			t.Errorf("the schema rejects %s: %v", line, err)
		}
	}
}
