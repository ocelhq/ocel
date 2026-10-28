package pulumi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"
	"go.opentelemetry.io/otel/codes"

	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

type stackSettingsStore struct {
	saveErr error
}

func (stackSettingsStore) StackSettings(context.Context, string) (*workspace.ProjectStack, error) {
	return &workspace.ProjectStack{}, nil
}

func (s stackSettingsStore) SaveStackSettings(context.Context, string, *workspace.ProjectStack) error {
	return s.saveErr
}

func TestAConfigureStackSpanEndsAsAnErrorWithItsSecretsMaskedWheneverConfiguringFails(t *testing.T) {
	t.Run("a config key the engine cannot read", func(t *testing.T) {
		ctx, spans := tracingtest.Record(t)
		err := applyConfig(ctx, stackSettingsStore{}, "prod", auto.ConfigMap{"no-namespace": {Value: "v"}}, nil)
		if err == nil {
			t.Fatal("applyConfig = nil, want the unreadable key refused")
		}

		got := tracingtest.SpanNamed(t, spans.Ended(), "configure stack").Status()
		if got.Code != codes.Error || !strings.Contains(got.Description, "no-namespace") {
			t.Errorf("status = %v %q, want an error naming the key", got.Code, got.Description)
		}
	})

	t.Run("a save whose error carries a secret the run knows", func(t *testing.T) {
		ctx, spans := tracingtest.Record(t)
		store := stackSettingsStore{saveErr: errors.New("write state with passphrase hunter2: denied")}
		if err := applyConfig(ctx, store, "prod", auto.ConfigMap{"aws:region": {Value: "eu-west-1"}}, []string{"hunter2"}); err == nil {
			t.Fatal("applyConfig = nil, want the failed save returned")
		}

		got := tracingtest.SpanNamed(t, spans.Ended(), "configure stack").Status()
		if got.Code != codes.Error || strings.Contains(got.Description, "hunter2") || !strings.Contains(got.Description, tracing.Redacted) {
			t.Errorf("status = %v %q, want an error with the passphrase masked", got.Code, got.Description)
		}
	})
}
