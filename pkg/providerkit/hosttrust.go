package providerkit

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
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

var (
	keyTypeShape = regexp.MustCompile(`^[a-z0-9-]+(@[a-z0-9.-]+)?$`)
	keyBlobShape = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)
	entryShape   = regexp.MustCompile(`^[A-Za-z0-9._:\-\[\]]+$`)
)

func (k HostKey) Fingerprinted() (HostKey, error) {
	if !keyTypeShape.MatchString(k.Type) {
		return k, fmt.Errorf("%q is not the name of an ssh host key type", k.Type)
	}
	if !keyBlobShape.MatchString(k.Key) {
		return k, fmt.Errorf("the offered %s key is not a base64 key blob", k.Type)
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(k.Key)
	if err != nil || len(blob) == 0 {
		return k, fmt.Errorf("the offered %s key is not a base64 key blob", k.Type)
	}
	sum := sha256.Sum256(blob)
	fingerprint := "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
	if k.Fingerprint != "" && k.Fingerprint != fingerprint {
		return k, fmt.Errorf("the offered %s key hashes to %s, not the %s the provider named", k.Type, fingerprint, k.Fingerprint)
	}
	k.Fingerprint = fingerprint
	return k, nil
}

func KnownHostsEntry(address, keyAlias string, port int) string {
	if keyAlias != "" {
		return keyAlias
	}
	if address == "" {
		return ""
	}
	if port == 0 || port == 22 {
		return address
	}
	return "[" + address + "]:" + strconv.Itoa(port)
}

func ValidKnownHostsEntry(entry string) bool { return entryShape.MatchString(entry) }

type HostTrust struct {
	Reason     HostTrustReason
	Host       string
	Address    string
	Port       int
	KeyAlias   string
	Got        HostKey
	Want       HostKey
	KnownHosts []string
	Remedy     string
}

func (t HostTrust) KnownHostsEntry() string {
	address := t.Address
	if address == "" {
		address = t.Host
	}
	return KnownHostsEntry(address, t.KeyAlias, t.Port)
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

func (t HostTrust) Message() string {
	var b strings.Builder
	if t.Reason == HostKeyMismatch {
		fmt.Fprintf(&b, "the host key for %s changed", t.Where())
		fmt.Fprintf(&b, "\n  got  %s %s", t.Got.Type, t.Got.Fingerprint)
		fmt.Fprintf(&b, "\n  want %s %s, held in %s", t.Want.Type, t.Want.Fingerprint, strings.Join(t.KnownHosts, ", "))
		fmt.Fprintf(&b, "\nEither that machine was rebuilt or something sits between you and it.\nIf it was rebuilt, drop the old key and try again:\n  %s", t.Remedy)
		return b.String()
	}
	b.WriteString(t.Offer())
	fmt.Fprintf(&b, "\nCheck that fingerprint against the machine itself, then record it:\n  %s", t.Remedy)
	return b.String()
}

func (t HostTrust) Offer() string {
	var b strings.Builder
	fmt.Fprintf(&b, "the host key for %s is in none of %s", t.Where(), strings.Join(t.KnownHosts, ", "))
	fmt.Fprintf(&b, "\n  %s %s", t.Got.Type, t.Got.Fingerprint)
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
		Remedy:     trust.Remedy,
		KeyAlias:   trust.KeyAlias,
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
		Remedy:     carried.GetRemedy(),
		KeyAlias:   carried.GetKeyAlias(),
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
