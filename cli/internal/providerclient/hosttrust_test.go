package providerclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/creack/pty"

	"github.com/ocelhq/ocel/cli/internal/terminal"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
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

func trustAsking(prompt Prompt, out io.Writer) Trust {
	return Trust{Prompt: prompt, Out: out}
}

type hostTrustFake struct {
	mode       string
	knownHosts string
	drives     string
}

func newHostTrustFake(t *testing.T, mode string) hostTrustFake {
	t.Helper()

	dir := t.TempDir()
	return hostTrustFake{
		mode:       mode,
		knownHosts: filepath.Join(dir, "home", ".ssh", "known_hosts"),
		drives:     filepath.Join(dir, "drives"),
	}
}

func (f hostTrustFake) call(t *testing.T, trust Trust) error {
	t.Helper()

	ctx, span, _ := deploySpan(t)
	p := startFake(t, ctx, f.mode, span, trust, f.env()...)
	_, err := Stream(ctx, p, "Bootstrap", &contractv1.BootstrapRequest{}, contractv1connect.ProviderServiceClient.Bootstrap)
	return err
}

func callRefusing(t *testing.T, trust Trust, call func() error) error {
	t.Helper()

	ctx, span, _ := deploySpan(t)
	p := startFake(t, ctx, "success", span, trust)
	return p.callTrusting(ctx, func(*Runner) error { return call() })
}

func (f hostTrustFake) env() []string {
	return []string{
		fakeProviderKnownHostsEnvVar + "=" + f.knownHosts,
		fakeProviderDrivesEnvVar + "=" + f.drives,
	}
}

func (f hostTrustFake) drivenTimes(t *testing.T) int {
	t.Helper()
	return len(f.drivenBy(t))
}

func (f hostTrustFake) drivenBy(t *testing.T) []string {
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

func (f hostTrustFake) recorded(t *testing.T) string {
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

func wantedLine() string {
	return fmt.Sprintf("[%s]:%d %s %s\n", fakeHostAddress, fakeHostPort, fakeHostKeyType, fakeHostKey)
}

func TestUnknownHostKeyKeepsTheRestOfKnownHostsIntact(t *testing.T) {
	t.Parallel()

	fake := newHostTrustFake(t, "unknown-host-key")
	if err := os.MkdirAll(filepath.Dir(fake.knownHosts), 0o700); err != nil {
		t.Fatalf("prepare the known_hosts directory: %v", err)
	}
	existing := "other.example.com ssh-ed25519 " + fakeOtherHostKey
	if err := os.WriteFile(fake.knownHosts, []byte(existing), 0o600); err != nil {
		t.Fatalf("seed known_hosts: %v", err)
	}

	trust := trustAsking(&scriptedPrompt{attended: true, answer: true}, io.Discard)
	if err := fake.call(t, trust); err != nil {
		t.Fatalf("call error = %v", err)
	}

	want := existing + "\n" + wantedLine()
	if got := fake.recorded(t); got != want {
		t.Errorf("known_hosts = %q, want %q", got, want)
	}
}

func TestUnknownHostKeyWithoutATTYNeverAsksAndIncludesTheRemedy(t *testing.T) {
	t.Parallel()

	fake := newHostTrustFake(t, "unknown-host-key")
	asker := &scriptedPrompt{attended: false, answer: true}

	err := fake.call(t, trustAsking(asker, io.Discard))
	if err == nil {
		t.Fatal("call error = nil, want a refusal with no TTY to decide on")
	}
	if len(asker.asked) != 0 {
		t.Errorf("asked %v, want no prompt without a TTY", asker.asked)
	}
	if !strings.Contains(err.Error(), fakeKey(fakeHostKey).Fingerprint) {
		t.Errorf("err = %v, want it to include the fingerprint", err)
	}
	if !strings.Contains(err.Error(), "ssh-keyscan") {
		t.Errorf("err = %v, want it to include the remedy", err)
	}
	if got := fake.recorded(t); got != "" {
		t.Errorf("known_hosts = %q, want nothing recorded", got)
	}
	if got := fake.drivenTimes(t); got != 1 {
		t.Errorf("the call ran %d times, want 1", got)
	}
}

func TestATrustBuiltOverAPipeNeverPromptsIntoABuffer(t *testing.T) {
	t.Parallel()

	fake := newHostTrustFake(t, "unknown-host-key")
	var log bytes.Buffer
	trust := Trust{Prompt: terminal.NewPrompt(&log, strings.NewReader("y\n")), Out: &log}

	err := fake.call(t, trust)
	if err == nil {
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

func TestATrustWhoseQuestionLandsWhereNobodyCanReadItNeverAsks(t *testing.T) {
	t.Parallel()

	fake := newHostTrustFake(t, "unknown-host-key")

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
	trust := Trust{Prompt: terminal.NewPrompt(&log, tty), Out: &log}

	if err := fake.call(t, trust); err == nil {
		t.Fatal("call error = nil, want a refusal when the question would go to a redirected stream")
	}
	if log.Len() != 0 {
		t.Errorf("wrote %q, want no key offered where the human reading the terminal cannot see it", log.String())
	}
	if got := fake.recorded(t); got != "" {
		t.Errorf("known_hosts = %q, want nothing recorded", got)
	}
	if got := fake.drivenTimes(t); got != 1 {
		t.Errorf("the call ran %d times, want 1", got)
	}
}

func TestATrustWithNoPromptNeverAsks(t *testing.T) {
	t.Parallel()

	fake := newHostTrustFake(t, "unknown-host-key")

	if err := fake.call(t, Trust{}); err == nil {
		t.Fatal("call error = nil, want the refusal returned with nobody to ask")
	}
	if got := fake.recorded(t); got != "" {
		t.Errorf("known_hosts = %q, want nothing recorded", got)
	}
}

func TestAPromptThatFailsStillIncludesTheRefusal(t *testing.T) {
	t.Parallel()

	fake := newHostTrustFake(t, "unknown-host-key")
	asker := &scriptedPrompt{attended: true, err: terminal.ErrStdinBusy}

	err := fake.call(t, trustAsking(asker, io.Discard))
	if !errors.Is(err, terminal.ErrStdinBusy) {
		t.Errorf("err = %v, want it to include the prompt's own failure", err)
	}
	if !strings.Contains(err.Error(), "ssh-keyscan") {
		t.Errorf("err = %v, want the refusal and its remedy kept alongside", err)
	}
}

func TestHostKeyMismatchNeverAsksAndNeverRetries(t *testing.T) {
	t.Parallel()

	for _, interactive := range []bool{true, false} {
		t.Run(fmt.Sprintf("interactive=%t", interactive), func(t *testing.T) {
			t.Parallel()

			fake := newHostTrustFake(t, "host-key-mismatch")
			asker := &scriptedPrompt{attended: interactive, answer: true}

			err := fake.call(t, trustAsking(asker, io.Discard))
			if err == nil {
				t.Fatal("call error = nil, want a mismatch to be terminal")
			}
			if len(asker.asked) != 0 {
				t.Errorf("asked %v, want no prompt for a mismatch", asker.asked)
			}
			if !strings.Contains(err.Error(), "ssh-keygen -R") {
				t.Errorf("err = %v, want it to include the ssh-keygen -R remedy", err)
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
	trust := trustAsking(asker, io.Discard)

	calls := 0
	if err := callRefusing(t, trust, func() error { calls++; return nil }); err != nil {
		t.Fatalf("call error = %v", err)
	}

	plain := errors.New("the provider fell over")
	err := callRefusing(t, trust, func() error { calls++; return plain })
	if !errors.Is(err, plain) {
		t.Errorf("call error = %v, want the call's own error untouched", err)
	}
	if calls != 2 {
		t.Errorf("called %d times, want 2", calls)
	}
	if len(asker.asked) != 0 {
		t.Errorf("asked %v, want no prompt when nothing refused on trust", asker.asked)
	}
}

func TestARetriedCallThatRefusesAgainNeverAsksTwice(t *testing.T) {
	t.Parallel()

	fake := newHostTrustFake(t, "unknown-host-key")
	asker := &scriptedPrompt{attended: true, answer: true}

	calls := 0
	refusal := provider.RefuseHostTrust(provider.HostTrust{
		Reason:     provider.UnknownHostKey,
		Host:       fakeHostName,
		Address:    fakeHostAddress,
		Port:       fakeHostPort,
		Got:        fakeKey(fakeHostKey),
		KnownHosts: []string{fake.knownHosts},
		Remedy:     "ssh-keyscan",
	})

	err := callRefusing(t, trustAsking(asker, io.Discard), func() error { calls++; return refusal })
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

func TestAKeyThatDoesNotHashToItsFingerprintIsNeverOffered(t *testing.T) {
	t.Parallel()

	store := filepath.Join(t.TempDir(), "known_hosts")
	asker := &scriptedPrompt{attended: true, answer: true}
	refusal := provider.RefuseHostTrust(provider.HostTrust{
		Reason:     provider.UnknownHostKey,
		Address:    fakeHostAddress,
		Port:       fakeHostPort,
		Got:        provider.HostKey{Type: fakeHostKeyType, Key: fakeHostKey, Fingerprint: "SHA256:not-the-hash-of-that-key"},
		KnownHosts: []string{store},
	})

	err := callRefusing(t, trustAsking(asker, io.Discard), func() error { return refusal })
	if err == nil {
		t.Fatal("call error = nil, want a key that betrays its fingerprint refused")
	}
	if len(asker.asked) != 0 {
		t.Errorf("asked %v, want no prompt for a key that does not hash to its fingerprint", asker.asked)
	}
	if _, statErr := os.Stat(store); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("known_hosts exists, want nothing recorded")
	}
}

func TestAProviderThatSpeaksInControlCharactersIsNeverOffered(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		trust provider.HostTrust
	}{
		{"a redressed key type", provider.HostTrust{Address: fakeHostAddress, Got: provider.HostKey{Type: "ssh-ed25519\n\033[2K  trusted", Key: fakeHostKey}}},
		{"a key blob containing a second entry", provider.HostTrust{Address: fakeHostAddress, Got: provider.HostKey{Type: fakeHostKeyType, Key: fakeHostKey + "\nevil.example.com ssh-ed25519 " + fakeOtherHostKey}}},
		{"an address containing a second entry", provider.HostTrust{Address: fakeHostAddress + "\nevil.example.com", Got: fakeKey(fakeHostKey)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := filepath.Join(t.TempDir(), "known_hosts")
			tc.trust.Reason = provider.UnknownHostKey
			tc.trust.KnownHosts = []string{store}
			asker := &scriptedPrompt{attended: true, answer: true}
			var out bytes.Buffer

			refusal := provider.RefuseHostTrust(tc.trust)
			err := callRefusing(t, trustAsking(asker, &out), func() error { return refusal })
			if err == nil {
				t.Fatal("call error = nil, want the offer refused")
			}
			if _, ok := provider.HostTrustOf(err); !ok {
				t.Errorf("err = %v, want the original refusal kept", err)
			}
			if len(asker.asked) != 0 {
				t.Errorf("asked %v, want nothing vouched for", asker.asked)
			}
			if out.Len() != 0 {
				t.Errorf("wrote %q, want nothing printed", out.String())
			}
			if _, statErr := os.Stat(store); !errors.Is(statErr, os.ErrNotExist) {
				t.Error("known_hosts exists, want nothing recorded")
			}
		})
	}
}

func TestKnownHostsStoreRefusesAStoreThatSwallowsWhatItIsGiven(t *testing.T) {
	t.Parallel()

	if _, err := os.Stat(os.DevNull); err != nil {
		t.Skipf("no %s to test against: %v", os.DevNull, err)
	}

	_, err := knownHostsStore(provider.HostTrust{KnownHosts: []string{os.DevNull}})
	if err == nil {
		t.Fatalf("knownHostsStore() error = nil, want %s refused as a store", os.DevNull)
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("err = %v, want it to say why nothing can be recorded there", err)
	}
}

func TestKnownHostsStoreFallsBackToTheUsersOwnFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := knownHostsStore(provider.HostTrust{})
	if err != nil {
		t.Fatalf("knownHostsStore() error = %v", err)
	}
	if want := filepath.Join(home, ".ssh", "known_hosts"); got != want {
		t.Errorf("knownHostsStore() = %q, want %q", got, want)
	}
}

func recordedLines(t *testing.T, store string) []string {
	t.Helper()

	content, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(content)), "\n")
}

func TestTrustingTheSameHostKeyTwiceLeavesOneLine(t *testing.T) {
	t.Parallel()

	store := filepath.Join(t.TempDir(), "known_hosts")
	for i := 0; i < 2; i++ {
		if err := record(store, wantedLine()); err != nil {
			t.Fatalf("record() error = %v", err)
		}
	}

	if lines := recordedLines(t, store); len(lines) != 1 {
		t.Errorf("known_hosts has %d lines (%v), want the entry recorded once", len(lines), lines)
	}
}

func TestAKeyAlreadyRecordedUnderAListOfNamesIsNotRecordedAgain(t *testing.T) {
	t.Parallel()

	store := filepath.Join(t.TempDir(), "known_hosts")
	existing := fmt.Sprintf("%s,[%s]:%d %s %s\n", fakeHostName, fakeHostAddress, fakeHostPort, fakeHostKeyType, fakeHostKey)
	if err := os.WriteFile(store, []byte(existing), 0o600); err != nil {
		t.Fatalf("seed known_hosts: %v", err)
	}

	if err := record(store, wantedLine()); err != nil {
		t.Fatalf("record() error = %v", err)
	}

	content, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	if string(content) != existing {
		t.Errorf("known_hosts = %q, want the line OpenSSH already has for that host left alone", content)
	}
}

func TestADifferentKeyForTheSameHostIsStillRecorded(t *testing.T) {
	t.Parallel()

	store := filepath.Join(t.TempDir(), "known_hosts")
	other := fmt.Sprintf("[%s]:%d %s %s\n", fakeHostAddress, fakeHostPort, fakeHostKeyType, fakeOtherHostKey)
	if err := record(store, other); err != nil {
		t.Fatalf("record() error = %v", err)
	}
	if err := record(store, wantedLine()); err != nil {
		t.Fatalf("record() error = %v", err)
	}

	if lines := recordedLines(t, store); len(lines) != 2 {
		t.Errorf("known_hosts has %d lines (%v), want a second key for the same host kept alongside the first", len(lines), lines)
	}
}

func TestRecordCreatesTheStoreForItsOwnerOnly(t *testing.T) {
	t.Parallel()

	store := filepath.Join(t.TempDir(), ".ssh", "known_hosts")
	if err := record(store, wantedLine()); err != nil {
		t.Fatalf("record() error = %v", err)
	}

	info, err := os.Stat(store)
	if err != nil {
		t.Fatalf("stat known_hosts: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("known_hosts mode = %v, want 0600", info.Mode().Perm())
	}
	dir, err := os.Stat(filepath.Dir(store))
	if err != nil {
		t.Fatalf("stat the known_hosts directory: %v", err)
	}
	if dir.Mode().Perm() != 0o700 {
		t.Errorf("directory mode = %v, want 0700", dir.Mode().Perm())
	}
}
