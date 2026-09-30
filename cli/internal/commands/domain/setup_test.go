package domain

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/consent"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/readiness"
	"github.com/ocelhq/ocel/cli/internal/run"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type said struct{ messages []string }

func (s *said) Receive(ev *streamv1.RunEvent) {
	if message := ev.GetOperation().GetMessage(); message != "" {
		s.messages = append(s.messages, message)
	}
}

func (*said) Close() error { return nil }

func setupSpan(t *testing.T, sink run.Sink) *run.Span {
	t.Helper()
	bus := run.NewBus(time.Now)
	if sink != nil {
		bus.Attach(sink)
	}
	_, running, err := bus.Begin(context.Background(), "ocel deploy", "")
	if err != nil {
		t.Fatal(err)
	}
	return running.Phase(progressv1.Phase_PHASE_CHECK)
}

func TestTheDomainSetupShowsTheSnippetInTheConfigsOwnFormatAndWaitsForTheEdit(t *testing.T) {
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
			policy := consent.Policy{Interactive: true, In: strings.NewReader("n\n"), Out: &out}
			missing := readiness.NoHostnameError{Slug: "shop", ConfigPath: tc.config}
			if err := NewSetup().Run(context.Background(), policy, setupSpan(t, nil), missing); !prerequisite.IsDeclined(err) {
				t.Fatalf("Run err = %v, want the stop reported as a decline", err)
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
	err := NewSetup().Run(context.Background(), policy, setupSpan(t, nil), readiness.NoHostnameError{Slug: "shop", ConfigPath: "/code/shop/ocel.json"})
	if !prerequisite.IsDeclined(err) {
		t.Errorf("Run err = %v, want the stop reported as a decline", err)
	}
}

func TestADomainSetupSaysWhichHostnameItReadOnceTheEditIsSaved(t *testing.T) {
	config := filepath.Join(t.TempDir(), "ocel.json")
	save := func() {
		if err := os.WriteFile(config, []byte(`{"slug": "shop", "provider": {"fake": {}}, "domains": {"production": "shop.example.com"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	policy := consent.Policy{Interactive: true, In: &savingOnRead{save: save, answer: "\n"}, Out: &out}
	seen := &said{}

	if err := NewSetup().Run(context.Background(), policy, setupSpan(t, seen), readiness.NoHostnameError{Slug: "shop", ConfigPath: config}); err != nil {
		t.Fatalf("Run err = %v, want Enter to go on", err)
	}
	if want := "Read shop.example.com from ocel.json"; !slices.Contains(seen.messages, want) {
		t.Errorf("said %q, want %q", seen.messages, want)
	}
}

type savingOnRead struct {
	save   func()
	answer string
}

func (r *savingOnRead) Read(p []byte) (int, error) {
	if r.save != nil {
		r.save()
		r.save = nil
	}
	if r.answer == "" {
		return 0, io.EOF
	}
	n := copy(p, r.answer)
	r.answer = r.answer[n:]
	return n, nil
}
