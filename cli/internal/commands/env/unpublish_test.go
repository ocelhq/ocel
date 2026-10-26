package env

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/outputschema"
	"github.com/ocelhq/ocel/cli/internal/outputschema/outputschematest"
	resultv1 "github.com/ocelhq/ocel/pkg/proto/cli/result/v1"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func envUnpublish(t *testing.T, root, written string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := runEnvUnpublish(context.Background(), newStreamedDependencies(&stderr), root, infraPublisher, strings.NewReader(written), envOptions{}, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func TestUnpublishingRemovesTheValuesStillAtTheVersionsThePublisherWrote(t *testing.T) {
	project := setUpEnvFixture(t)
	if _, stderr, err := envPublish(t, project.Root, `{"values":[{"key":"LOG_LEVEL","value":"debug"}]}`, envOptions{}); err != nil {
		t.Fatalf("runEnvPublish err = %v; stderr=%s", err, stderr)
	}

	stdout, stderr, err := envUnpublish(t, project.Root, `{"values":[{"key":"LOG_LEVEL","version":"1"}]}`)
	if err != nil {
		t.Fatalf("runEnvUnpublish err = %v; stderr=%s", err, stderr)
	}
	if !strings.Contains(stdout, "LOG_LEVEL") {
		t.Errorf("unpublish stdout = %q, want the removed key named", stdout)
	}
	if _, err := valuesOf(project).Get(context.Background(), production(), cellAt("LOG_LEVEL"), false); !errors.Is(err, variablestore.ErrNotFound) {
		t.Errorf("LOG_LEVEL = %v, want it removed", err)
	}
}

func TestUnpublishingAVersionThePublisherHasSinceRewrittenLeavesTheValue(t *testing.T) {
	project := setUpEnvFixture(t)
	for range 2 {
		if _, stderr, err := envPublish(t, project.Root, `{"values":[{"key":"LOG_LEVEL","value":"debug"}]}`, envOptions{}); err != nil {
			t.Fatalf("runEnvPublish err = %v; stderr=%s", err, stderr)
		}
	}

	var stdout, stderr bytes.Buffer
	if err := runEnvUnpublish(context.Background(), newJSONDependencies(&stderr), project.Root, infraPublisher, strings.NewReader(`{"values":[{"key":"LOG_LEVEL","version":"1"}]}`), envOptions{}, &stdout, &stderr); err != nil {
		t.Fatalf("runEnvUnpublish err = %v; stderr=%s", err, stderr.String())
	}
	schema, err := outputschema.ComposeResult(new(resultv1.EnvUnpublishResult).ProtoReflect().Descriptor().FullName())
	if err != nil {
		t.Fatal(err)
	}
	if err := outputschematest.Validate(outputschematest.Compile(t, schema), stdout.Bytes()); err != nil {
		t.Errorf("the env unpublish schema rejects what env unpublish prints: %v\n%s", err, stdout.String())
	}
	if strings.Contains(stdout.String(), "LOG_LEVEL") {
		t.Errorf("stdout = %s, want nothing removed", stdout.String())
	}
	if got, err := valuesOf(project).Get(context.Background(), production(), cellAt("LOG_LEVEL"), true); err != nil || got.Plaintext != "debug" || got.Version != 2 {
		t.Errorf("LOG_LEVEL = %+v, %v, want version 2 kept", got, err)
	}
}
