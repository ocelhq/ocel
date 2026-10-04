package terminal

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

func TestAFailureOutsideAnyRunReadsLikeTheSummaryOfOneThatFailed(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	var out bytes.Buffer
	PrintFailure(&out, errors.New("no ocel.json found in this directory or any parent\nrun `ocel init` to set up this project"))

	want := "✗ no ocel.json found in this directory or any parent\n" +
		"  run `ocel init` to set up this project\n"
	if got := out.String(); got != want {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

func TestAFailureUnderJSONIsOneLineHoldingTheErrorObject(t *testing.T) {
	var out bytes.Buffer
	PrintFailureJSON(&out, &streamv1.RunError{
		Code:      "project.no_config",
		Message:   "no ocel.json",
		Hint:      proto.String("run `ocel init`"),
		DocsUrl:   proto.String("https://ocel.dev/docs/errors/project.no_config"),
		Retryable: true,
	})

	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("output %q is not one JSON document: %v", out.String(), err)
	}
	want := map[string]any{"ok": false, "error": map[string]any{
		"code":      "project.no_config",
		"message":   "no ocel.json",
		"hint":      "run `ocel init`",
		"docsUrl":   "https://ocel.dev/docs/errors/project.no_config",
		"retryable": true,
	}}
	if !reflect.DeepEqual(doc, want) {
		t.Errorf("document = %v, want %v", doc, want)
	}
	if lines := strings.Count(out.String(), "\n"); lines != 1 || !strings.HasSuffix(out.String(), "\n") {
		t.Errorf("output = %q, want exactly one line", out.String())
	}
}

func TestAFailureUnderJSONThatCannotBeEncodedStillPrintsOneInternalDocument(t *testing.T) {
	var out bytes.Buffer
	PrintFailureJSON(&out, &streamv1.RunError{Code: "project.no_config", Message: "no \xff.json"})

	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("output %q is not one JSON document: %v", out.String(), err)
	}
	failure, _ := doc["error"].(map[string]any)
	if doc["ok"] != false || failure["code"] != "internal" || failure["message"] == "" {
		t.Errorf("document = %v, want ok:false and an internal error with a message", doc)
	}
}
