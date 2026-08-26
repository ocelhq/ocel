package providerkit

import (
	"errors"
	"fmt"
	"strings"

	connect "connectrpc.com/connect"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type HostTrustReason string

const (
	UnknownHostKey  HostTrustReason = "unknown-host-key"
	HostKeyMismatch HostTrustReason = "host-key-mismatch"
)

type HostKey struct {
	Type        string
	Key         string
	Fingerprint string
}

func (k HostKey) IsZero() bool { return k == HostKey{} }

type HostTrust struct {
	Reason     HostTrustReason
	Host       string
	Address    string
	Port       int
	Got        HostKey
	Want       HostKey
	KnownHosts []string
}

func (t HostTrust) Terminal() bool { return t.Reason == HostKeyMismatch }

func (t HostTrust) Where() string {
	place := t.Address
	if t.Host != "" && t.Host != t.Address {
		place = fmt.Sprintf("%s (%s)", t.Host, t.Address)
	}
	if t.Port == 0 {
		return place
	}
	return fmt.Sprintf("%s port %d", place, t.Port)
}

func (t HostTrust) Remedy() string {
	if t.Reason == HostKeyMismatch {
		return fmt.Sprintf("ssh-keygen -R %s -f %s", t.entry(), t.trustStore())
	}
	return fmt.Sprintf("ssh-keyscan -p %d %s >> %s", t.port(), t.Address, t.trustStore())
}

func (t HostTrust) trustStore() string {
	if len(t.KnownHosts) == 0 {
		return "~/.ssh/known_hosts"
	}
	return t.KnownHosts[0]
}

func (t HostTrust) entry() string {
	if t.port() == 22 {
		return t.Address
	}
	return fmt.Sprintf("'[%s]:%d'", t.Address, t.Port)
}

func (t HostTrust) port() int {
	if t.Port == 0 {
		return 22
	}
	return t.Port
}

func (t HostTrust) Message() string {
	var b strings.Builder
	if t.Reason == HostKeyMismatch {
		fmt.Fprintf(&b, "the host key for %s changed", t.Where())
		fmt.Fprintf(&b, "\n  got  %s %s", t.Got.Type, t.Got.Fingerprint)
		fmt.Fprintf(&b, "\n  want %s %s, held in %s", t.Want.Type, t.Want.Fingerprint, strings.Join(t.KnownHosts, ", "))
		fmt.Fprintf(&b, "\nEither that machine was rebuilt or something sits between you and it.\nIf it was rebuilt, drop the old key and try again:\n  %s", t.Remedy())
		return b.String()
	}
	fmt.Fprintf(&b, "the host key for %s is in none of %s", t.Where(), strings.Join(t.KnownHosts, ", "))
	fmt.Fprintf(&b, "\n  %s %s", t.Got.Type, t.Got.Fingerprint)
	fmt.Fprintf(&b, "\nCheck that fingerprint against the machine itself, then record it:\n  %s", t.Remedy())
	return b.String()
}

type HostTrustRefusal struct {
	Refusal
	Trust HostTrust
}

func (r HostTrustRefusal) Unwrap() error { return r.Refusal }

func RefuseHostTrust(trust HostTrust) error {
	return HostTrustRefusal{
		Refusal: Refusal{Code: CodeDenied, Message: trust.Message()},
		Trust:   trust,
	}
}

func HostTrustOf(err error) (HostTrust, bool) {
	var refusal HostTrustRefusal
	if errors.As(err, &refusal) {
		return refusal.Trust, true
	}
	var wire *connect.Error
	if !errors.As(err, &wire) {
		return HostTrust{}, false
	}
	for _, detail := range wire.Details() {
		value, err := detail.Value()
		if err != nil {
			continue
		}
		if carried, ok := value.(*contractv1.HostTrustRefusal); ok {
			return hostTrustFrom(carried), true
		}
	}
	return HostTrust{}, false
}

var hostTrustReasons = map[HostTrustReason]contractv1.HostTrustReason{
	UnknownHostKey:  contractv1.HostTrustReason_HOST_TRUST_REASON_UNKNOWN_HOST_KEY,
	HostKeyMismatch: contractv1.HostTrustReason_HOST_TRUST_REASON_HOST_KEY_MISMATCH,
}

func HostTrustProto(trust HostTrust) *contractv1.HostTrustRefusal {
	return &contractv1.HostTrustRefusal{
		Reason:     hostTrustReasons[trust.Reason],
		Host:       trust.Host,
		Address:    trust.Address,
		Port:       uint32(trust.Port),
		Got:        hostKeyProto(trust.Got),
		Want:       hostKeyProto(trust.Want),
		KnownHosts: trust.KnownHosts,
		Remedy:     trust.Remedy(),
	}
}

func hostKeyProto(key HostKey) *contractv1.HostKey {
	if key.IsZero() {
		return nil
	}
	return &contractv1.HostKey{Type: key.Type, Key: key.Key, Fingerprint: key.Fingerprint}
}

func hostTrustFrom(carried *contractv1.HostTrustRefusal) HostTrust {
	trust := HostTrust{
		Host:       carried.GetHost(),
		Address:    carried.GetAddress(),
		Port:       int(carried.GetPort()),
		Got:        hostKeyFrom(carried.GetGot()),
		Want:       hostKeyFrom(carried.GetWant()),
		KnownHosts: carried.GetKnownHosts(),
	}
	for reason, encoded := range hostTrustReasons {
		if encoded == carried.GetReason() {
			trust.Reason = reason
		}
	}
	return trust
}

func hostTrustError(refusal HostTrustRefusal) error {
	wire := connect.NewError(connect.CodePermissionDenied, errors.New(refusal.Message))
	if detail, err := connect.NewErrorDetail(HostTrustProto(refusal.Trust)); err == nil {
		wire.AddDetail(detail)
	}
	return wire
}

func hostKeyFrom(carried *contractv1.HostKey) HostKey {
	if carried == nil {
		return HostKey{}
	}
	return HostKey{Type: carried.GetType(), Key: carried.GetKey(), Fingerprint: carried.GetFingerprint()}
}
