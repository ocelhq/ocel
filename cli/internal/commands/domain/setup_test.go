package domain

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func setupSpan(t *testing.T) *run.Span {
	t.Helper()
	_, running, err := run.NewBus(time.Now).Begin(context.Background(), "ocel deploy", "")
	if err != nil {
		t.Fatal(err)
	}
	return running.Phase(progressv1.Phase_PHASE_CHECK)
}

func TestTheDomainSetupShowsTheSnippetInTheConfigsOwnFormatAndWaitsForEnter(t *testing.T) {
	for _, tc := range []struct {
		config string
		want   string
	}{
		{"/code/shop/ocel.json", `"domains": { "production": "shop.example.com" }`},
		{"/code/shop/ocel.yaml", "domains:\n      production: shop.example.com"},
		{"/code/shop/ocel.config.ts", `domains: { production: "shop.example.com" },`},
	} {
		t.Run(tc.config, func(t *testing.T) {
			var out bytes.Buffer
			policy := consent.Policy{Interactive: true, In: strings.NewReader("\n"), Out: &out}
			missing := readiness.NoHostnameError{Slug: "shop", ConfigPath: tc.config}
			if err := NewSetup().Run(context.Background(), policy, setupSpan(t), missing); err != nil {
				t.Fatalf("Run err = %v, want Enter to go on", err)
			}
			if !strings.Contains(out.String(), tc.want) {
				t.Errorf("shown %q, want the snippet %q", out.String(), tc.want)
			}
			if !strings.Contains(out.String(), "Press Enter once it's saved") {
				t.Errorf("shown %q, want it to wait for the edit", out.String())
			}
		})
	}
}

func TestADomainSetupStoppedAtTheSnippetIsADecline(t *testing.T) {
	var out bytes.Buffer
	policy := consent.Policy{Interactive: true, In: strings.NewReader("n\n"), Out: &out}
	err := NewSetup().Run(context.Background(), policy, setupSpan(t), readiness.NoHostnameError{Slug: "shop", ConfigPath: "/code/shop/ocel.json"})
	if !prerequisite.IsDeclined(err) {
		t.Errorf("Run err = %v, want the stop reported as a decline", err)
	}
}
