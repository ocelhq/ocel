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
