package session

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const recordableKey = "AAAAC3NzaC1lZDI1NTE5AAAAIGjxLv2WrJFcWFzVC/ui/P691jGR92crO0DsjeqiPi54"

func recordable(t *testing.T) (HostTrust, string) {
	t.Helper()
	store := filepath.Join(t.TempDir(), ".ssh", "known_hosts")
	trust := unknownHostKey()
	trust.Got = HostKey{Type: "ssh-ed25519", Key: recordableKey}
	trust.KnownHosts = []string{store}
	return trust, store
}

func TestAnUnknownHostKeyAsksTheUserAndAYesRecordsItInTheirKnownHosts(t *testing.T) {
	t.Parallel()

	trust, store := recordable(t)
	err := RefuseHostTrust(trust)
	if code, ok := provider.RefusedCode(err); !ok || code != refusal.CodeDenied {
		t.Errorf("RefuseHostTrust() = %v, want a %s refusal while nobody has answered", err, refusal.CodeDenied)
	}
	if !strings.Contains(err.Error(), trust.Remedy) {
		t.Errorf("RefuseHostTrust() = %q, want the remedy for a run nobody can answer", err)
	}
	question, asked := provider.QuestionOf(err)
	if !asked {
		t.Fatal("RefuseHostTrust() over an unknown key asked nothing")
	}
	if !strings.Contains(question.Finding, "SHA256:") || !strings.Contains(question.Prompt, store) {
		t.Errorf("question = %+v, want the fingerprint shown and the file it would write named", question)
	}
	if _, err := os.Stat(store); err == nil {
		t.Fatal("known_hosts was written before anyone answered")
	}

	if err := question.Confirm(context.Background()); err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	recorded, err := os.ReadFile(store)
	if err != nil {
		t.Fatalf("a yes recorded nothing: %v", err)
	}
	if want := "[203.0.113.10]:2222 ssh-ed25519 " + recordableKey + "\n"; string(recorded) != want {
		t.Errorf("known_hosts = %q, want %q", recorded, want)
	}
}

func TestAChangedHostKeyIsRefusedAndAsksNothing(t *testing.T) {
	t.Parallel()

	err := RefuseHostTrust(changedHostKey())
	if _, asked := provider.QuestionOf(err); asked {
		t.Error("a changed host key was offered as a question rather than refused")
	}
	if code, ok := provider.RefusedCode(err); !ok || code != refusal.CodeDenied {
		t.Errorf("RefuseHostTrust() = %v, want a %s refusal", err, refusal.CodeDenied)
	}
	if trust, ok := HostTrustOf(err); !ok || trust.Reason != HostKeyMismatch {
		t.Errorf("HostTrustOf() = %+v, %t, want the mismatch it refused over", trust, ok)
	}
}

func TestAnUnknownKeyNothingCanRecordIsRefusedWithoutAQuestion(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		shape func(*HostTrust)
	}{
		{"a store that swallows what it is given", func(trust *HostTrust) { trust.KnownHosts = []string{os.DevNull} }},
		{"a redressed key type", func(trust *HostTrust) { trust.Got.Type = "ssh-ed25519\n\033[2K  trusted" }},
		{"a key blob containing a second entry", func(trust *HostTrust) {
			trust.Got.Key = recordableKey + "\nevil.example.com ssh-ed25519 " + recordableKey
		}},
		{"an address containing a second entry", func(trust *HostTrust) { trust.Address += "\nevil.example.com" }},
		{"a fingerprint the key does not hash to", func(trust *HostTrust) { trust.Got.Fingerprint = "SHA256:not-the-hash-of-that-key" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			trust, store := recordable(t)
			tc.shape(&trust)
			err := RefuseHostTrust(trust)
			if _, asked := provider.QuestionOf(err); asked {
				t.Errorf("RefuseHostTrust() asked the user to vouch for %s", tc.name)
			}
			if code, ok := provider.RefusedCode(err); !ok || code != refusal.CodeDenied {
				t.Errorf("RefuseHostTrust() = %v, want a %s refusal", err, refusal.CodeDenied)
			}
			if _, statErr := os.Stat(store); statErr == nil {
				t.Error("known_hosts was written")
			}
		})
	}
}

func unknownHostKey() HostTrust {
	return HostTrust{
		Reason:     UnknownHostKey,
		Host:       "web-1",
		Address:    "203.0.113.10",
		Port:       2222,
		Got:        HostKey{Type: "ssh-ed25519", Key: "AAAAC3NzaC1lZDI1NTE5AAAAIGot", Fingerprint: "SHA256:got"},
		KnownHosts: []string{"/home/ada/.ssh/known_hosts"},
		Remedy:     "ssh-keyscan -t ssh-ed25519 -p 2222 203.0.113.10 >> /home/ada/.ssh/known_hosts",
	}
}

func changedHostKey() HostTrust {
	trust := unknownHostKey()
	trust.Reason = HostKeyMismatch
	trust.Want = HostKey{Type: "ssh-ed25519", Key: "AAAAC3NzaC1lZDI1NTE5AAAAIWant", Fingerprint: "SHA256:want"}
	trust.Remedy = "ssh-keygen -R '[203.0.113.10]:2222' -f /home/ada/.ssh/known_hosts"
	return trust
}

func TestAnUnknownHostKeyIsRecoverableAndNamesTheFingerprint(t *testing.T) {
	t.Parallel()

	trust := unknownHostKey()
	if trust.Terminal() {
		t.Error("Terminal() = true, want an unknown host key to be answerable by trusting it")
	}
	message := trust.Message()
	for _, want := range []string{"web-1", "203.0.113.10", "port 2222", "SHA256:got", "/home/ada/.ssh/known_hosts"} {
		if !strings.Contains(message, want) {
			t.Errorf("Message() = %q, want it to include %q", message, want)
		}
	}
	if !strings.Contains(message, trust.Remedy) {
		t.Errorf("Message() = %q, want it to spell out the remedy %q", message, trust.Remedy)
	}
}

func TestAChangedHostKeyIsTerminalAndIncludesTheKeygenRemedy(t *testing.T) {
	t.Parallel()

	trust := changedHostKey()
	if !trust.Terminal() {
		t.Error("Terminal() = false, want a changed host key to end the run")
	}
	message := trust.Message()
	for _, want := range []string{"SHA256:got", "SHA256:want"} {
		if !strings.Contains(message, want) {
			t.Errorf("Message() = %q, want it to include %q", message, want)
		}
	}
	if !strings.Contains(message, trust.Remedy) {
		t.Errorf("Message() = %q, want it to spell out the remedy %q", message, trust.Remedy)
	}
}

func TestTheKnownHostsEntryIsTheNameSshKeysOn(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		trust HostTrust
		want  string
	}{
		{"the default port stays bare", HostTrust{Address: "203.0.113.10", Port: 22}, "203.0.113.10"},
		{"an unstated port stays bare", HostTrust{Address: "203.0.113.10"}, "203.0.113.10"},
		{"another port is bracketed", HostTrust{Address: "203.0.113.10", Port: 2222}, "[203.0.113.10]:2222"},
		{"the written host takes the place of a missing address", HostTrust{Host: "web-1", Port: 22}, "web-1"},
		{"a key alias wins over the address", HostTrust{Address: "203.0.113.10", Port: 2222, KeyAlias: "ocel-vps"}, "ocel-vps"},
		{"nothing named keys on nothing", HostTrust{Port: 2222}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.trust.KnownHostsEntry(); got != tc.want {
				t.Errorf("KnownHostsEntry() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOnlyAConservativeNameCanKeyAKnownHostsEntry(t *testing.T) {
	t.Parallel()

	for _, entry := range []string{"203.0.113.10", "[203.0.113.10]:2222", "web-1.example.com", "2001:db8::1"} {
		if !ValidKnownHostsEntry(entry) {
			t.Errorf("ValidKnownHostsEntry(%q) = false, want a name ssh itself writes accepted", entry)
		}
	}
	for _, entry := range []string{"", "host name", "host\nevil.example.com", "host\rx", "host\033[2K", "host;rm -rf /"} {
		if ValidKnownHostsEntry(entry) {
			t.Errorf("ValidKnownHostsEntry(%q) = true, want it refused", entry)
		}
	}
}

func TestAKeyIsFingerprintedOnlyWhenItIsShapedLikeOne(t *testing.T) {
	t.Parallel()

	const blob = "AAAAC3NzaC1lZDI1NTE5AAAAIGjxLv2WrJFcWFzVC/ui/P691jGR92crO0DsjeqiPi54"

	key, err := (HostKey{Type: "ssh-ed25519", Key: blob}).Fingerprinted()
	if err != nil {
		t.Fatalf("Fingerprinted() error = %v", err)
	}
	if !strings.HasPrefix(key.Fingerprint, "SHA256:") {
		t.Errorf("Fingerprinted() = %q, want the SHA256 form ssh prints", key.Fingerprint)
	}

	for _, tc := range []struct {
		name string
		key  HostKey
	}{
		{"an unnamed type", HostKey{Key: blob}},
		{"a type containing an escape", HostKey{Type: "ssh-ed25519\033[2K", Key: blob}},
		{"a type containing a newline", HostKey{Type: "ssh-ed25519\nx", Key: blob}},
		{"a blob containing a newline", HostKey{Type: "ssh-ed25519", Key: blob + "\n" + blob}},
		{"a blob containing a space", HostKey{Type: "ssh-ed25519", Key: blob + " x"}},
		{"an empty blob", HostKey{Type: "ssh-ed25519"}},
		{"a fingerprint the blob does not hash to", HostKey{Type: "ssh-ed25519", Key: blob, Fingerprint: "SHA256:nope"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := tc.key.Fingerprinted(); err == nil {
				t.Errorf("Fingerprinted() error = nil, want %s refused", tc.name)
			}
		})
	}
}

func TestTheOfferIsTheRefusalWithoutTheRemedy(t *testing.T) {
	t.Parallel()

	trust := unknownHostKey()
	if !strings.HasPrefix(trust.Message(), trust.Offer()) {
		t.Errorf("Message() = %q, want it to open with the offer %q", trust.Message(), trust.Offer())
	}
	if strings.Contains(trust.Offer(), trust.Remedy) {
		t.Errorf("Offer() = %q, want the ssh-keyscan remedy left out", trust.Offer())
	}
}
