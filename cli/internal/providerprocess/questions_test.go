package providerprocess

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/creack/pty"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

type scriptedPrompt struct {
	attended bool
	answer   bool
	err      error
	asked    []string
}

func (a *scriptedPrompt) Attended() bool { return a.attended }

func (a *scriptedPrompt) Confirm(_ context.Context, question string) (bool, error) {
	a.asked = append(a.asked, question)
	return a.answer, a.err
}

func answering(prompt Prompt, out io.Writer) Questions {
	return Questions{Prompt: prompt, Out: out}
}

type questionFake struct {
	mode       string
	knownHosts string
	drives     string
}

func newQuestionFake(t *testing.T, mode string) questionFake {
	t.Helper()

	dir := t.TempDir()
	return questionFake{
		mode:       mode,
		knownHosts: filepath.Join(dir, "home", ".ssh", "known_hosts"),
		drives:     filepath.Join(dir, "drives"),
	}
}

func (f questionFake) call(t *testing.T, questions Questions) error {
	t.Helper()

	ctx, span, _ := deploySpan(t)
	p := startFake(t, ctx, f.mode, span, questions, f.env()...)
	_, err := Stream(ctx, p, "Bootstrap", &contractv1.BootstrapRequest{}, contractv1connect.ProviderServiceClient.Bootstrap)
	return err
}

func callRefusing(t *testing.T, questions Questions, call func() error) error {
	t.Helper()
	return callRefusingIn(t, "success", questions, call)
}

func callRefusingIn(t *testing.T, mode string, questions Questions, call func() error) error {
	t.Helper()

	ctx, span, _ := deploySpan(t)
	p := startFake(t, ctx, mode, span, questions)
	return p.callAnswering(ctx, func(*Process) error { return call() })
}

func (f questionFake) env() []string {
	return []string{
		fakeProviderKnownHostsEnvVar + "=" + f.knownHosts,
		fakeProviderDrivesEnvVar + "=" + f.drives,
	}
}

func (f questionFake) drivenTimes(t *testing.T) int {
	t.Helper()
	return len(f.drivenBy(t))
}

func (f questionFake) drivenBy(t *testing.T) []string {
	t.Helper()

	content, err := os.ReadFile(f.drives)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read the drive log: %v", err)
	}
	return strings.Fields(string(content))
}

func (f questionFake) recorded(t *testing.T) string {
	t.Helper()

	content, err := os.ReadFile(f.knownHosts)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	return string(content)
}

func asking(id string) error {
	wire := connect.NewError(connect.CodePermissionDenied, errors.New("the host key is unknown; record it with ssh-keyscan"))
	detail, err := connect.NewErrorDetail(&contractv1.Question{Id: id, Finding: "the host key is unknown", Prompt: "Trust it?"})
	if err != nil {
		panic(err)
	}
	wire.AddDetail(detail)
	return wire
}

func TestAQuestionNobodyCanSeeIsNeverAskedAndTheRefusalCarriesTheRemedy(t *testing.T) {
	t.Parallel()

	fake := newQuestionFake(t, "unknown-host-key")
	asker := &scriptedPrompt{attended: false, answer: true}

	err := fake.call(t, answering(asker, io.Discard))
	if err == nil {
		t.Fatal("call error = nil, want a refusal with no TTY to decide on")
	}
	if len(asker.asked) != 0 {
		t.Errorf("asked %v, want no prompt without a TTY", asker.asked)
	}
	if !strings.Contains(err.Error(), fakeHostFingerprint) {
		t.Errorf("err = %v, want it to include the fingerprint", err)
	}
	if !strings.Contains(err.Error(), "ssh-keyscan") {
		t.Errorf("err = %v, want it to include the remedy", err)
	}
	if strings.Contains(err.Error(), "connection lost") {
		t.Errorf("err = %v, want a refusal read as a refusal, not a dropped provider", err)
	}
	if got := fake.recorded(t); got != "" {
		t.Errorf("known_hosts = %q, want nothing recorded", got)
	}
	if got := fake.drivenTimes(t); got != 1 {
		t.Errorf("the call ran %d times, want 1", got)
	}
}

func TestAQuestionNobodyCanAnswerReportsInputRequired(t *testing.T) {
	t.Parallel()

	for name, questions := range map[string]Questions{
		"no terminal":      answering(&scriptedPrompt{attended: false, answer: true}, io.Discard),
		"under --json":     {Prompt: &scriptedPrompt{attended: true, answer: true}, Out: io.Discard, IsJSON: func() bool { return true }},
		"no prompt at all": {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := newQuestionFake(t, "unknown-host-key")

			err := fake.call(t, questions)
			if err == nil {
				t.Fatal("call error = nil, want a refusal")
			}
			if got := clierror.NewRunError(err).GetCode(); got != clierror.CodeInputRequired {
				t.Errorf("code = %q, want %q", got, clierror.CodeInputRequired)
			}
			if !strings.Contains(err.Error(), "ssh-keyscan") {
				t.Errorf("err = %v, want the provider's remedy kept", err)
			}
		})
	}
}

func TestAQuestionAPersonDeclinesReportsConfirmationRequiredWithTheProvidersRemedy(t *testing.T) {
	t.Parallel()

	fake := newQuestionFake(t, "unknown-host-key")

	err := fake.call(t, answering(&scriptedPrompt{attended: true, answer: false}, io.Discard))
	if err == nil {
		t.Fatal("call error = nil, want the declined refusal")
	}
	got := clierror.NewRunError(err)
	if got.GetCode() != clierror.CodeConfirmationRequired || got.Hint != nil {
		t.Errorf("run error = %v, want confirmation_required with no hint, since --yes never answers a provider question", got)
	}
	if !strings.Contains(got.GetMessage(), "ssh-keyscan") {
		t.Errorf("message = %q, want the provider's remedy kept", got.GetMessage())
	}
	if recorded := fake.recorded(t); recorded != "" {
		t.Errorf("known_hosts = %q, want nothing recorded once declined", recorded)
	}
}

func TestAQuestionUnderJSONIsNeverAskedOnATerminal(t *testing.T) {
	t.Parallel()

	fake := newQuestionFake(t, "unknown-host-key")
	asker := &scriptedPrompt{attended: true, answer: true}
	var out bytes.Buffer

	err := fake.call(t, Questions{Prompt: asker, Out: &out, IsJSON: func() bool { return true }})
	if err == nil {
		t.Fatal("call error = nil, want a refusal")
	}
	if len(asker.asked) != 0 || out.Len() != 0 {
		t.Errorf("asked %v and wrote %q, want nothing asked", asker.asked, out.String())
	}
	if got := fake.recorded(t); got != "" {
		t.Errorf("known_hosts = %q, want nothing recorded", got)
	}
	if got := fake.drivenTimes(t); got != 1 {
		t.Errorf("the call ran %d times, want 1", got)
	}
}

func TestQuestionsBuiltOverAPipeNeverPromptIntoABuffer(t *testing.T) {
	t.Parallel()

	fake := newQuestionFake(t, "unknown-host-key")
	var log bytes.Buffer
	questions := Questions{Prompt: terminal.NewPrompt(&log, strings.NewReader("y\n")), Out: &log}

	if err := fake.call(t, questions); err == nil {
		t.Fatal("call error = nil, want a refusal when neither end is a terminal")
	}
	if log.Len() != 0 {
		t.Errorf("wrote %q, want nothing offered into a stream that is not a terminal", log.String())
	}
	if got := fake.recorded(t); got != "" {
		t.Errorf("known_hosts = %q, want nothing recorded", got)
	}
	if got := fake.drivenTimes(t); got != 1 {
		t.Errorf("the call ran %d times, want 1", got)
	}
}

func TestAQuestionThatLandsWhereNobodyCanReadItIsNeverAsked(t *testing.T) {
	t.Parallel()

	fake := newQuestionFake(t, "unknown-host-key")

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	t.Cleanup(func() {
		ptmx.Close()
		tty.Close()
	})

	if _, err := ptmx.WriteString("n\n"); err != nil {
		t.Fatalf("write to the pty: %v", err)
	}

	var log bytes.Buffer
	questions := Questions{Prompt: terminal.NewPrompt(&log, tty), Out: &log}

	if err := fake.call(t, questions); err == nil {
		t.Fatal("call error = nil, want a refusal when the question would go to a redirected stream")
	}
	if log.Len() != 0 {
		t.Errorf("wrote %q, want nothing offered where the human reading the terminal cannot see it", log.String())
	}
	if got := fake.recorded(t); got != "" {
		t.Errorf("known_hosts = %q, want nothing recorded", got)
	}
	if got := fake.drivenTimes(t); got != 1 {
		t.Errorf("the call ran %d times, want 1", got)
	}
}

func TestQuestionsWithNoPromptNeverAsk(t *testing.T) {
	t.Parallel()

	fake := newQuestionFake(t, "unknown-host-key")

	if err := fake.call(t, Questions{}); err == nil {
		t.Fatal("call error = nil, want the refusal returned with nobody to ask")
	}
	if got := fake.recorded(t); got != "" {
		t.Errorf("known_hosts = %q, want nothing recorded", got)
	}
}

func TestAPromptThatFailsStillIncludesTheRefusal(t *testing.T) {
	t.Parallel()

	fake := newQuestionFake(t, "unknown-host-key")
	asker := &scriptedPrompt{attended: true, err: terminal.ErrStdinBusy}

	err := fake.call(t, answering(asker, io.Discard))
	if !errors.Is(err, terminal.ErrStdinBusy) {
		t.Errorf("err = %v, want it to include the prompt's own failure", err)
	}
	if !strings.Contains(err.Error(), "ssh-keyscan") {
		t.Errorf("err = %v, want the refusal and its remedy kept alongside", err)
	}
}

func TestARefusalThatAsksNothingIsNeverAskedAndNeverRetried(t *testing.T) {
	t.Parallel()

	for _, interactive := range []bool{true, false} {
		t.Run(fmt.Sprintf("interactive=%t", interactive), func(t *testing.T) {
			t.Parallel()

			fake := newQuestionFake(t, "host-key-mismatch")
			asker := &scriptedPrompt{attended: interactive, answer: true}

			err := fake.call(t, answering(asker, io.Discard))
			if err == nil {
				t.Fatal("call error = nil, want the provider's refusal")
			}
			if len(asker.asked) != 0 {
				t.Errorf("asked %v, want no prompt for a refusal that asks nothing", asker.asked)
			}
			if !strings.Contains(err.Error(), "ssh-keygen -R") {
				t.Errorf("err = %v, want it to include the provider's remedy", err)
			}
			if got := fake.recorded(t); got != "" {
				t.Errorf("known_hosts = %q, want nothing recorded", got)
			}
			if got := fake.drivenTimes(t); got != 1 {
				t.Errorf("the call ran %d times, want 1", got)
			}
		})
	}
}

func TestACallThatNeverRefusesIsLeftAlone(t *testing.T) {
	t.Parallel()

	asker := &scriptedPrompt{attended: true, answer: true}
	questions := answering(asker, io.Discard)

	calls := 0
	if err := callRefusing(t, questions, func() error { calls++; return nil }); err != nil {
		t.Fatalf("call error = %v", err)
	}

	plain := errors.New("the provider fell over")
	err := callRefusing(t, questions, func() error { calls++; return plain })
	if !errors.Is(err, plain) {
		t.Errorf("call error = %v, want the call's own error untouched", err)
	}
	if calls != 2 {
		t.Errorf("called %d times, want 2", calls)
	}
	if len(asker.asked) != 0 {
		t.Errorf("asked %v, want no prompt when nothing asked a question", asker.asked)
	}
}

func TestARetriedCallThatAsksAgainIsNeverAskedTwice(t *testing.T) {
	t.Parallel()

	asker := &scriptedPrompt{attended: true, answer: true}

	calls := 0
	err := callRefusing(t, answering(asker, io.Discard), func() error { calls++; return asking(fakeQuestionID) })
	if err == nil {
		t.Fatal("call error = nil, want the second refusal returned")
	}
	if calls != 2 {
		t.Errorf("called %d times, want at most one retry", calls)
	}
	if len(asker.asked) != 1 {
		t.Errorf("asked %d times, want exactly one prompt", len(asker.asked))
	}
}

type answersInTurn struct {
	answers []bool
	asked   []string
}

func (a *answersInTurn) Attended() bool { return true }

func (a *answersInTurn) Confirm(_ context.Context, question string) (bool, error) {
	a.asked = append(a.asked, question)
	answer := a.answers[0]
	a.answers = a.answers[1:]
	return answer, nil
}

func TestAConfirmThatAsksAgainIsAskedBeforeTheCallIsRetried(t *testing.T) {
	t.Parallel()

	asker := &scriptedPrompt{attended: true, answer: true}

	calls := 0
	err := callRefusingIn(t, "asks-again-on-confirm", answering(asker, io.Discard), func() error {
		calls++
		if calls == 1 {
			return asking(fakeQuestionID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("call error = %v, want the call retried once the follow-up is confirmed", err)
	}
	if want := []string{"Trust it?", "Have you opened port 443?"}; !slices.Equal(asker.asked, want) {
		t.Errorf("asked %q, want %q", asker.asked, want)
	}
	if calls != 2 {
		t.Errorf("called %d times, want one retry after the last answer", calls)
	}
}

func TestDecliningTheQuestionAConfirmAsksAgainRetriesNothing(t *testing.T) {
	t.Parallel()

	asker := &answersInTurn{answers: []bool{true, false}}

	calls := 0
	err := callRefusingIn(t, "asks-again-on-confirm", answering(asker, io.Discard), func() error { calls++; return asking(fakeQuestionID) })
	if got := clierror.NewRunError(err).GetCode(); got != clierror.CodeConfirmationRequired {
		t.Fatalf("call error = %v (code %q), want %q", err, got, clierror.CodeConfirmationRequired)
	}
	if calls != 1 {
		t.Errorf("called %d times, want no retry once the follow-up is declined", calls)
	}
}

func TestAConfirmTheProviderRefusesRetriesNothing(t *testing.T) {
	t.Parallel()

	asker := &scriptedPrompt{attended: true, answer: true}

	calls := 0
	err := callRefusing(t, answering(asker, io.Discard), func() error { calls++; return asking("not-a-question-it-asked") })
	if err == nil || !strings.Contains(err.Error(), "confirm") {
		t.Fatalf("call error = %v, want the provider's refusal to confirm alongside the question", err)
	}
	if calls != 1 {
		t.Errorf("called %d times, want no retry once the provider refused the answer", calls)
	}
}

func TestAProviderThatSpeaksInControlCharactersIsShownNone(t *testing.T) {
	t.Parallel()

	asker := &scriptedPrompt{attended: true, answer: false}
	var out bytes.Buffer
	wire := connect.NewError(connect.CodePermissionDenied, errors.New("no"))
	detail, err := connect.NewErrorDetail(&contractv1.Question{
		Id:      fakeQuestionID,
		Finding: "the host key\033[2K is\r fine\n  ssh-ed25519 SHA256:x",
		Prompt:  "Trust\033[1A it?",
	})
	if err != nil {
		t.Fatal(err)
	}
	wire.AddDetail(detail)

	if err := callRefusing(t, answering(asker, &out), func() error { return wire }); err == nil {
		t.Fatal("call error = nil, want the refusal kept after a no")
	}
	if strings.ContainsAny(out.String(), "\033\r") || len(asker.asked) != 1 || strings.ContainsRune(asker.asked[0], '\033') {
		t.Errorf("showed %q and asked %q, want the provider's text without control characters", out.String(), asker.asked)
	}
	if !strings.Contains(out.String(), "\n  ssh-ed25519 SHA256:x") {
		t.Errorf("showed %q, want the finding's own lines kept", out.String())
	}
}
