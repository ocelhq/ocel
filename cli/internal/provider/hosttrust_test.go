package provider

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

	"github.com/ocelhq/ocel/cli/internal/prompt"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

type scriptedAsker struct {
	interactive bool
	answer      bool
	err         error
	asked       []string
}

func (a *scriptedAsker) Interactive() bool { return a.interactive }

func (a *scriptedAsker) Confirm(_ context.Context, question string) (bool, error) {
	a.asked = append(a.asked, question)
	return a.answer, a.err
}

func trustAsking(asker Confirmer, out io.Writer) Trust {
	return Trust{Ask: asker, Out: out}
}

type hostTrustFake struct {
	knownHosts string
	drives     string
	drive      func() error
}

func fakeHostTrustDrive(t *testing.T, ctx context.Context, mode string) hostTrustFake {
	t.Helper()

	dir := t.TempDir()
	fake := hostTrustFake{
		knownHosts: filepath.Join(dir, "home", ".ssh", "known_hosts"),
		drives:     filepath.Join(dir, "drives"),
	}
	fake.drive = func() error {
		runner, _ := spawnFake(t, ctx, mode, Config{Env: []string{
			fakeProviderKnownHostsEnvVar + "=" + fake.knownHosts,
			fakeProviderDrivesEnvVar + "=" + fake.drives,
		}})
		defer runner.Close()
		if err := runner.Ready(ctx); err != nil {
			return err
		}
		return Stream(ctx, runner, "Bootstrap", &contractv1.BootstrapRequest{}, contractv1connect.ProviderServiceClient.Bootstrap, nil)
	}
	return fake
}

func (f hostTrustFake) drivenTimes(t *testing.T) int {
	t.Helper()

	content, err := os.ReadFile(f.drives)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read the drive log: %v", err)
	}
	return len(strings.Fields(string(content)))
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

func TestUnknownHostKeyOnATTYRecordsTheKeyAndRedrivesOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fake := fakeHostTrustDrive(t, ctx, "unknown-host-key")
	asker := &scriptedAsker{interactive: true, answer: true}
	var out bytes.Buffer

	if err := driveTrusting(ctx, trustAsking(asker, &out), fake.drive); err != nil {
		t.Fatalf("driveTrusting() error = %v, want the re-driven command to succeed", err)
	}

	if len(asker.asked) != 1 {
		t.Errorf("asked %d times (%v), want exactly one prompt", len(asker.asked), asker.asked)
	}
	if got := fake.drivenTimes(t); got != 2 {
		t.Errorf("provider driven %d times, want 2", got)
	}
	if got := fake.recorded(t); got != wantedLine() {
		t.Errorf("known_hosts = %q, want %q", got, wantedLine())
	}
	if !strings.Contains(out.String(), fakeKey(fakeHostKey).Fingerprint) {
		t.Errorf("output = %q, want it to show the fingerprint before asking", out.String())
	}
}

func TestUnknownHostKeyKeepsTheRestOfKnownHostsIntact(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fake := fakeHostTrustDrive(t, ctx, "unknown-host-key")
	if err := os.MkdirAll(filepath.Dir(fake.knownHosts), 0o700); err != nil {
		t.Fatalf("prepare the known_hosts directory: %v", err)
	}
	existing := "other.example.com ssh-ed25519 " + fakeOtherHostKey
	if err := os.WriteFile(fake.knownHosts, []byte(existing), 0o600); err != nil {
		t.Fatalf("seed known_hosts: %v", err)
	}

	trust := trustAsking(&scriptedAsker{interactive: true, answer: true}, io.Discard)
	if err := driveTrusting(ctx, trust, fake.drive); err != nil {
		t.Fatalf("driveTrusting() error = %v", err)
	}

	want := existing + "\n" + wantedLine()
	if got := fake.recorded(t); got != want {
		t.Errorf("known_hosts = %q, want %q", got, want)
	}
}

func TestUnknownHostKeyRefusedAtThePromptRecordsNothing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fake := fakeHostTrustDrive(t, ctx, "unknown-host-key")
	asker := &scriptedAsker{interactive: true, answer: false}

	err := driveTrusting(ctx, trustAsking(asker, io.Discard), fake.drive)
	if err == nil {
		t.Fatal("driveTrusting() error = nil, want the refusal to stand")
	}
	if trust, ok := providerkit.HostTrustOf(err); !ok || trust.Reason != providerkit.UnknownHostKey {
		t.Errorf("err = %v, want it to still carry the unknown-host-key refusal", err)
	}
	if got := fake.recorded(t); got != "" {
		t.Errorf("known_hosts = %q, want nothing recorded", got)
	}
	if got := fake.drivenTimes(t); got != 1 {
		t.Errorf("provider driven %d times, want 1", got)
	}
}

func TestUnknownHostKeyWithoutATTYNeverAsksAndCarriesTheRemedy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fake := fakeHostTrustDrive(t, ctx, "unknown-host-key")
	asker := &scriptedAsker{interactive: false, answer: true}

	err := driveTrusting(ctx, trustAsking(asker, io.Discard), fake.drive)
	if err == nil {
		t.Fatal("driveTrusting() error = nil, want a refusal with no TTY to decide on")
	}
	if len(asker.asked) != 0 {
		t.Errorf("asked %v, want no prompt without a TTY", asker.asked)
	}
	if !strings.Contains(err.Error(), fakeKey(fakeHostKey).Fingerprint) {
		t.Errorf("err = %v, want it to carry the fingerprint", err)
	}
	if !strings.Contains(err.Error(), "ssh-keyscan") {
		t.Errorf("err = %v, want it to carry the remedy", err)
	}
	if got := fake.recorded(t); got != "" {
		t.Errorf("known_hosts = %q, want nothing recorded", got)
	}
	if got := fake.drivenTimes(t); got != 1 {
		t.Errorf("provider driven %d times, want 1", got)
	}
}

func TestATrustBuiltOverAPipeNeverPromptsIntoABuffer(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fake := fakeHostTrustDrive(t, ctx, "unknown-host-key")
	var log bytes.Buffer
	trust := Trust{Ask: prompt.New(&log, strings.NewReader("y\n")), Out: &log}

	err := driveTrusting(ctx, trust, fake.drive)
	if err == nil {
		t.Fatal("driveTrusting() error = nil, want a refusal when neither end is a terminal")
	}
	if log.Len() != 0 {
		t.Errorf("wrote %q, want nothing offered into a stream that is not a terminal", log.String())
	}
	if got := fake.recorded(t); got != "" {
		t.Errorf("known_hosts = %q, want nothing recorded", got)
	}
	if got := fake.drivenTimes(t); got != 1 {
		t.Errorf("provider driven %d times, want 1", got)
	}
}

func TestATrustWithNoConfirmerNeverAsks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fake := fakeHostTrustDrive(t, ctx, "unknown-host-key")

	if err := driveTrusting(ctx, Trust{}, fake.drive); err == nil {
		t.Fatal("driveTrusting() error = nil, want the refusal to stand with nobody to ask")
	}
	if got := fake.recorded(t); got != "" {
		t.Errorf("known_hosts = %q, want nothing recorded", got)
	}
}

func TestTheLiveViewIsSuspendedForTheLengthOfThePrompt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fake := fakeHostTrustDrive(t, ctx, "unknown-host-key")
	suspended, resumed := 0, 0
	trust := trustAsking(&scriptedAsker{interactive: true, answer: false}, io.Discard)
	trust.Suspend = func() func() {
		suspended++
		return func() { resumed++ }
	}

	if err := driveTrusting(ctx, trust, fake.drive); err == nil {
		t.Fatal("driveTrusting() error = nil, want the refusal to stand")
	}
	if suspended != 1 || resumed != 1 {
		t.Errorf("suspended %d times and resumed %d, want one of each around the prompt", suspended, resumed)
	}
}

func TestAPromptThatFailsStillCarriesTheRefusal(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fake := fakeHostTrustDrive(t, ctx, "unknown-host-key")
	asker := &scriptedAsker{interactive: true, err: prompt.ErrStdinBusy}

	err := driveTrusting(ctx, trustAsking(asker, io.Discard), fake.drive)
	if !errors.Is(err, prompt.ErrStdinBusy) {
		t.Errorf("err = %v, want it to carry the prompt's own failure", err)
	}
	if !strings.Contains(err.Error(), "ssh-keyscan") {
		t.Errorf("err = %v, want the refusal and its remedy kept alongside", err)
	}
}

func TestHostKeyMismatchNeverAsksAndNeverRedrives(t *testing.T) {
	t.Parallel()

	for _, interactive := range []bool{true, false} {
		t.Run(fmt.Sprintf("interactive=%t", interactive), func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			fake := fakeHostTrustDrive(t, ctx, "host-key-mismatch")
			asker := &scriptedAsker{interactive: interactive, answer: true}

			err := driveTrusting(ctx, trustAsking(asker, io.Discard), fake.drive)
			if err == nil {
				t.Fatal("driveTrusting() error = nil, want a mismatch to be terminal")
			}
			if len(asker.asked) != 0 {
				t.Errorf("asked %v, want no prompt for a mismatch", asker.asked)
			}
			if !strings.Contains(err.Error(), "ssh-keygen -R") {
				t.Errorf("err = %v, want it to carry the ssh-keygen -R remedy", err)
			}
			if got := fake.recorded(t); got != "" {
				t.Errorf("known_hosts = %q, want nothing recorded", got)
			}
			if got := fake.drivenTimes(t); got != 1 {
				t.Errorf("provider driven %d times, want 1", got)
			}
		})
	}
}

func TestDriveThatNeverRefusesIsLeftAlone(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	asker := &scriptedAsker{interactive: true, answer: true}
	trust := trustAsking(asker, io.Discard)

	drives := 0
	if err := driveTrusting(ctx, trust, func() error { drives++; return nil }); err != nil {
		t.Fatalf("driveTrusting() error = %v", err)
	}

	plain := errors.New("the provider fell over")
	err := driveTrusting(ctx, trust, func() error { drives++; return plain })
	if !errors.Is(err, plain) {
		t.Errorf("driveTrusting() error = %v, want the drive's own error untouched", err)
	}
	if drives != 2 {
		t.Errorf("drove %d times, want 2", drives)
	}
	if len(asker.asked) != 0 {
		t.Errorf("asked %v, want no prompt when nothing refused on trust", asker.asked)
	}
}

func TestARedriveThatRefusesAgainNeverAsksTwice(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fake := fakeHostTrustDrive(t, ctx, "unknown-host-key")
	asker := &scriptedAsker{interactive: true, answer: true}

	drives := 0
	refusal := providerkit.RefuseHostTrust(providerkit.HostTrust{
		Reason:     providerkit.UnknownHostKey,
		Host:       fakeHostName,
		Address:    fakeHostAddress,
		Port:       fakeHostPort,
		Got:        fakeKey(fakeHostKey),
		KnownHosts: []string{fake.knownHosts},
		Remedy:     "ssh-keyscan",
	})

	err := driveTrusting(ctx, trustAsking(asker, io.Discard), func() error { drives++; return refusal })
	if err == nil {
		t.Fatal("driveTrusting() error = nil, want the second refusal to stand")
	}
	if drives != 2 {
		t.Errorf("drove %d times, want at most one retry", drives)
	}
	if len(asker.asked) != 1 {
		t.Errorf("asked %d times, want exactly one prompt", len(asker.asked))
	}
}

func TestAKeyThatDoesNotHashToItsFingerprintIsNeverOffered(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := filepath.Join(t.TempDir(), "known_hosts")
	asker := &scriptedAsker{interactive: true, answer: true}
	refusal := providerkit.RefuseHostTrust(providerkit.HostTrust{
		Reason:     providerkit.UnknownHostKey,
		Address:    fakeHostAddress,
		Port:       fakeHostPort,
		Got:        providerkit.HostKey{Type: fakeHostKeyType, Key: fakeHostKey, Fingerprint: "SHA256:not-the-hash-of-that-key"},
		KnownHosts: []string{store},
	})

	err := driveTrusting(ctx, trustAsking(asker, io.Discard), func() error { return refusal })
	if err == nil {
		t.Fatal("driveTrusting() error = nil, want a key that betrays its fingerprint refused")
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

	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		trust providerkit.HostTrust
	}{
		{"a redressed key type", providerkit.HostTrust{Address: fakeHostAddress, Got: providerkit.HostKey{Type: "ssh-ed25519\n\033[2K  trusted", Key: fakeHostKey}}},
		{"a key blob carrying a second entry", providerkit.HostTrust{Address: fakeHostAddress, Got: providerkit.HostKey{Type: fakeHostKeyType, Key: fakeHostKey + "\nevil.example.com ssh-ed25519 " + fakeOtherHostKey}}},
		{"an address carrying a second entry", providerkit.HostTrust{Address: fakeHostAddress + "\nevil.example.com", Got: fakeKey(fakeHostKey)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			store := filepath.Join(t.TempDir(), "known_hosts")
			tc.trust.Reason = providerkit.UnknownHostKey
			tc.trust.KnownHosts = []string{store}
			asker := &scriptedAsker{interactive: true, answer: true}
			var out bytes.Buffer

			refusal := providerkit.RefuseHostTrust(tc.trust)
			err := driveTrusting(ctx, trustAsking(asker, &out), func() error { return refusal })
			if err == nil {
				t.Fatal("driveTrusting() error = nil, want the offer refused")
			}
			if _, ok := providerkit.HostTrustOf(err); !ok {
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

	_, err := knownHostsStore(providerkit.HostTrust{KnownHosts: []string{os.DevNull}})
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

	got, err := knownHostsStore(providerkit.HostTrust{})
	if err != nil {
		t.Fatalf("knownHostsStore() error = %v", err)
	}
	if want := filepath.Join(home, ".ssh", "known_hosts"); got != want {
		t.Errorf("knownHostsStore() = %q, want %q", got, want)
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
