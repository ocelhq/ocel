package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const otherRecordableKey = "AAAAC3NzaC1lZDI1NTE5AAAAIBtlSdtoFVwXcyx7e4GZ4N/zr7JGNG3D6kjc2ceBg1Ag"

func wantedLine() string {
	return "[203.0.113.7]:2222 ssh-ed25519 " + recordableKey + "\n"
}

func recordedLines(t *testing.T, store string) []string {
	t.Helper()

	content, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(content)), "\n")
}

func TestRecordingAHostKeyKeepsTheRestOfKnownHostsIntact(t *testing.T) {
	t.Parallel()

	store := filepath.Join(t.TempDir(), "known_hosts")
	existing := "other.example.com ssh-ed25519 " + otherRecordableKey
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
	if want := existing + "\n" + wantedLine(); string(content) != want {
		t.Errorf("known_hosts = %q, want %q", content, want)
	}
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
	existing := "vps.example.com,[203.0.113.7]:2222 ssh-ed25519 " + recordableKey + "\n"
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
	if err := record(store, "[203.0.113.7]:2222 ssh-ed25519 "+otherRecordableKey+"\n"); err != nil {
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

func TestKnownHostsStoreRefusesAStoreThatSwallowsWhatItIsGiven(t *testing.T) {
	t.Parallel()

	if _, err := os.Stat(os.DevNull); err != nil {
		t.Skipf("no %s to test against: %v", os.DevNull, err)
	}

	_, err := knownHostsStore([]string{os.DevNull})
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

	got, err := knownHostsStore(nil)
	if err != nil {
		t.Fatalf("knownHostsStore() error = %v", err)
	}
	if want := filepath.Join(home, ".ssh", "known_hosts"); got != want {
		t.Errorf("knownHostsStore() = %q, want %q", got, want)
	}
}
