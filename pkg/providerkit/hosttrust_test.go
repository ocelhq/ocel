package providerkit_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	connect "connectrpc.com/connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func unknownHostKey() providerkit.HostTrust {
	return providerkit.HostTrust{
		Reason:     providerkit.UnknownHostKey,
		Host:       "web-1",
		Address:    "203.0.113.10",
		Port:       2222,
		Got:        providerkit.HostKey{Type: "ssh-ed25519", Key: "AAAAC3NzaC1lZDI1NTE5AAAAIGot", Fingerprint: "SHA256:got"},
		KnownHosts: []string{"/home/ada/.ssh/known_hosts"},
		Remedy:     "ssh-keyscan -t ssh-ed25519 -p 2222 203.0.113.10 >> /home/ada/.ssh/known_hosts",
	}
}

func changedHostKey() providerkit.HostTrust {
	trust := unknownHostKey()
	trust.Reason = providerkit.HostKeyMismatch
	trust.Want = providerkit.HostKey{Type: "ssh-ed25519", Key: "AAAAC3NzaC1lZDI1NTE5AAAAIWant", Fingerprint: "SHA256:want"}
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
			t.Errorf("Message() = %q, want it to carry %q", message, want)
		}
	}
	if !strings.Contains(message, trust.Remedy) {
		t.Errorf("Message() = %q, want it to spell out the remedy %q", message, trust.Remedy)
	}
}

func TestAChangedHostKeyIsTerminalAndCarriesTheKeygenRemedy(t *testing.T) {
	t.Parallel()

	trust := changedHostKey()
	if !trust.Terminal() {
		t.Error("Terminal() = false, want a changed host key to end the run")
	}
	message := trust.Message()
	for _, want := range []string{"SHA256:got", "SHA256:want"} {
		if !strings.Contains(message, want) {
			t.Errorf("Message() = %q, want it to carry %q", message, want)
		}
	}
	if !strings.Contains(message, trust.Remedy) {
		t.Errorf("Message() = %q, want it to spell out the remedy %q", message, trust.Remedy)
	}
}

func TestATrustRefusalIsDeniedAndReadsBackWhole(t *testing.T) {
	t.Parallel()

	for _, trust := range []providerkit.HostTrust{unknownHostKey(), changedHostKey()} {
		t.Run(string(trust.Reason), func(t *testing.T) {
			err := providerkit.RefuseHostTrust(trust)
			var refusal providerkit.Refusal
			if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeDenied {
				t.Fatalf("RefuseHostTrust() = %v, want a %s refusal", err, providerkit.CodeDenied)
			}
			read, ok := providerkit.HostTrustOf(err)
			if !ok {
				t.Fatal("HostTrustOf() found no trust decision in a trust refusal")
			}
			if !reflect.DeepEqual(read, trust) {
				t.Errorf("HostTrustOf() = %+v, want %+v", read, trust)
			}
		})
	}
}

func TestATrustRefusalSurvivesTheWire(t *testing.T) {
	t.Parallel()

	for _, trust := range []providerkit.HostTrust{unknownHostKey(), changedHostKey()} {
		t.Run(string(trust.Reason), func(t *testing.T) {
			wire := providerkit.RefusalError(providerkit.RefuseHostTrust(trust))
			if connect.CodeOf(wire) != connect.CodePermissionDenied {
				t.Errorf("RefusalError() carried %s, want %s", connect.CodeOf(wire), connect.CodePermissionDenied)
			}
			read, ok := providerkit.HostTrustOf(wire)
			if !ok {
				t.Fatal("HostTrustOf() found no trust decision on the far side of the wire")
			}
			if !reflect.DeepEqual(read, trust) {
				t.Errorf("HostTrustOf() = %+v, want %+v", read, trust)
			}
		})
	}
}

func TestAnOrdinaryRefusalCarriesNoTrustDecision(t *testing.T) {
	t.Parallel()

	for _, err := range []error{
		providerkit.Refuse(providerkit.CodeDenied, "no"),
		providerkit.RefusalError(providerkit.Refuse(providerkit.CodeDenied, "no")),
		fmt.Errorf("wrapped: %w", errors.New("no")),
	} {
		if _, ok := providerkit.HostTrustOf(err); ok {
			t.Errorf("HostTrustOf(%v) read a trust decision out of an error that holds none", err)
		}
	}
}

func TestTheKnownHostsEntryIsTheNameSshKeysOn(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		trust providerkit.HostTrust
		want  string
	}{
		{"the default port stays bare", providerkit.HostTrust{Address: "203.0.113.10", Port: 22}, "203.0.113.10"},
		{"an unstated port stays bare", providerkit.HostTrust{Address: "203.0.113.10"}, "203.0.113.10"},
		{"another port is bracketed", providerkit.HostTrust{Address: "203.0.113.10", Port: 2222}, "[203.0.113.10]:2222"},
		{"the written host stands in for a missing address", providerkit.HostTrust{Host: "web-1", Port: 22}, "web-1"},
		{"a key alias wins over the address", providerkit.HostTrust{Address: "203.0.113.10", Port: 2222, KeyAlias: "ocel-vps"}, "ocel-vps"},
		{"nothing named keys on nothing", providerkit.HostTrust{Port: 2222}, ""},
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
		if !providerkit.ValidKnownHostsEntry(entry) {
			t.Errorf("ValidKnownHostsEntry(%q) = false, want a name ssh itself writes accepted", entry)
		}
	}
	for _, entry := range []string{"", "host name", "host\nevil.example.com", "host\rx", "host\033[2K", "host;rm -rf /"} {
		if providerkit.ValidKnownHostsEntry(entry) {
			t.Errorf("ValidKnownHostsEntry(%q) = true, want it refused", entry)
		}
	}
}

func TestAKeyIsFingerprintedOnlyWhenItIsShapedLikeOne(t *testing.T) {
	t.Parallel()

	const blob = "AAAAC3NzaC1lZDI1NTE5AAAAIGjxLv2WrJFcWFzVC/ui/P691jGR92crO0DsjeqiPi54"

	key, err := (providerkit.HostKey{Type: "ssh-ed25519", Key: blob}).Fingerprinted()
	if err != nil {
		t.Fatalf("Fingerprinted() error = %v", err)
	}
	if !strings.HasPrefix(key.Fingerprint, "SHA256:") {
		t.Errorf("Fingerprinted() = %q, want the SHA256 form ssh prints", key.Fingerprint)
	}

	for _, tc := range []struct {
		name string
		key  providerkit.HostKey
	}{
		{"an unnamed type", providerkit.HostKey{Key: blob}},
		{"a type carrying an escape", providerkit.HostKey{Type: "ssh-ed25519\033[2K", Key: blob}},
		{"a type carrying a newline", providerkit.HostKey{Type: "ssh-ed25519\nx", Key: blob}},
		{"a blob carrying a newline", providerkit.HostKey{Type: "ssh-ed25519", Key: blob + "\n" + blob}},
		{"a blob carrying a space", providerkit.HostKey{Type: "ssh-ed25519", Key: blob + " x"}},
		{"an empty blob", providerkit.HostKey{Type: "ssh-ed25519"}},
		{"a fingerprint the blob does not hash to", providerkit.HostKey{Type: "ssh-ed25519", Key: blob, Fingerprint: "SHA256:nope"}},
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
