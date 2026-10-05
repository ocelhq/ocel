package clierror_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

func TestAnErrorWithoutACodeBecomesAnInternalRunErrorWithItsMessageAndNoDocsURL(t *testing.T) {
	got := clierror.NewRunError(errors.New("the upload was refused"))

	want := &streamv1.RunError{Code: "internal", Message: "the upload was refused"}
	if !proto.Equal(got, want) {
		t.Fatalf("run error = %s, want %s", protojson.Format(got), protojson.Format(want))
	}
}

func TestACodedErrorBecomesARunErrorWithItsCodeHintRetryAndDocsPage(t *testing.T) {
	got := clierror.NewRunError(&clierror.Error{Code: "project.no_config", Hint: "run `ocel init`", Retryable: true, Cause: errors.New("no ocel.json")})

	want := &streamv1.RunError{
		Code:      "project.no_config",
		Message:   "no ocel.json",
		Hint:      proto.String("run `ocel init`"),
		DocsUrl:   proto.String("https://ocel.dev/docs/errors/project.no_config"),
		Retryable: true,
	}
	if !proto.Equal(got, want) {
		t.Fatalf("run error = %s, want %s", protojson.Format(got), protojson.Format(want))
	}
}

func TestAWrappedCodedErrorKeepsItsCodeAndTheWholeWrappedMessage(t *testing.T) {
	got := clierror.NewRunError(fmt.Errorf("reading project: %w", &clierror.Error{Code: "project.no_config", Cause: errors.New("no ocel.json")}))

	if got.GetCode() != "project.no_config" || got.GetMessage() != "reading project: no ocel.json" {
		t.Fatalf("run error = %s, want the inner code and the wrapped text", protojson.Format(got))
	}
}

func TestACodedErrorWithItsOwnMessageReportsItWhateverWrapsItAndReadsAsItsCause(t *testing.T) {
	err := fmt.Errorf("deploy: %w", &clierror.Error{Code: "project.no_config", Message: "No ocel.json here.", Cause: errors.New("no ocel.json found.\nRun `ocel init` and try again")})

	if got := clierror.NewRunError(err); got.GetMessage() != "No ocel.json here." {
		t.Fatalf("run error = %s, want the coded error's own message", protojson.Format(got))
	}
	if err.Error() != "deploy: no ocel.json found.\nRun `ocel init` and try again" {
		t.Errorf("Error() = %q, want the cause's text for the human output", err.Error())
	}
}

func TestACodedErrorWithoutACauseReadsAsItsCode(t *testing.T) {
	err := &clierror.Error{Code: "project.no_config"}
	if err.Error() != "project.no_config" {
		t.Fatalf("Error() = %q, want the code", err.Error())
	}
}

func TestACodeOutsideTheRegistryBecomesInternalWithNoDocsPageKeepingItsHintAndRetry(t *testing.T) {
	validator, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"", "internal", "project.unregistered", "Project.NoConfig", "project..no_config", "project.no-config", ".project", "1project", "project.", "project no_config"} {
		got := clierror.NewRunError(&clierror.Error{Code: code, Hint: "try again", Retryable: true, Cause: errors.New("boom")})

		want := &streamv1.RunError{Code: "internal", Message: "boom", Hint: proto.String("try again"), Retryable: true}
		if !proto.Equal(got, want) {
			t.Errorf("code %q: run error = %s, want %s", code, protojson.Format(got), protojson.Format(want))
		}
		if err := validator.Validate(got); err != nil {
			t.Errorf("code %q: run error breaks the proto's rules: %v", code, err)
		}
	}
}

func TestAMissingInputReportsInputRequiredWithTheFlagOrEnvVarThatSuppliesIt(t *testing.T) {
	got := clierror.NewRunError(fmt.Errorf("link: %w", clierror.NewInputRequired(errors.New("multiple organizations found"), "--org <slug>")))

	want := &streamv1.RunError{
		Code:    clierror.CodeInputRequired,
		Message: "link: multiple organizations found",
		Hint:    proto.String("--org <slug>"),
		DocsUrl: proto.String("https://ocel.dev/docs/errors/input_required"),
	}
	if !proto.Equal(got, want) {
		t.Fatalf("run error = %s, want %s", protojson.Format(got), protojson.Format(want))
	}
}

func TestAnInterruptBecomesAnInterruptedRunErrorWhateverCodeItWraps(t *testing.T) {
	for name, err := range map[string]error{
		"cancelled":           fmt.Errorf("read ocel.config.ts: %w", context.Canceled),
		"exit 130":            &exitcode.ExitError{Code: exitcode.Interrupt},
		"coded and cancelled": &clierror.Error{Code: "provider.unavailable", Hint: "retry", Cause: context.Canceled},
	} {
		got := clierror.NewRunError(err)

		if got.GetCode() != "interrupted" || got.DocsUrl != nil || got.Hint != nil {
			t.Errorf("%s: run error = %s, want code interrupted with no docs page or hint", name, protojson.Format(got))
		}
	}
}

func TestNoErrorBecomesNoRunError(t *testing.T) {
	if got := clierror.NewRunError(nil); got != nil {
		t.Fatalf("run error = %v, want nil", got)
	}
}
