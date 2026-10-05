package session

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
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
		return k, fmt.Errorf("the offered %s key hashes to %s, not the %s it was named with", k.Type, fingerprint, k.Fingerprint)
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
		fmt.Fprintf(&b, "\n  want %s %s, recorded in %s", t.Want.Type, t.Want.Fingerprint, strings.Join(t.KnownHosts, ", "))
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
	error
	Trust HostTrust
}

func (r HostTrustRefusal) Unwrap() error { return r.error }

func RefuseHostTrust(trust HostTrust) error {
	denied := refusal.Refuse(refusal.CodeDenied, "%s", trust.Message())
	if trust.Terminal() {
		return HostTrustRefusal{error: denied, Trust: trust}
	}
	question, err := newRecordHostKeyQuestion(trust)
	if err != nil {
		return HostTrustRefusal{error: errors.Join(denied, err), Trust: trust}
	}
	return HostTrustRefusal{error: provider.Ask(trust.Message(), question), Trust: trust}
}

func newRecordHostKeyQuestion(trust HostTrust) (provider.Question, error) {
	offered, err := trust.Got.Fingerprinted()
	if err != nil {
		return provider.Question{}, err
	}
	entry := trust.KnownHostsEntry()
	if !ValidKnownHostsEntry(entry) {
		return provider.Question{}, fmt.Errorf("%q is not a name a known_hosts entry can be keyed on", entry)
	}
	store, err := knownHostsStore(trust.KnownHosts)
	if err != nil {
		return provider.Question{}, err
	}
	trust.Got = offered
	if len(trust.KnownHosts) == 0 {
		trust.KnownHosts = []string{store}
	}
	line := entry + " " + offered.Type + " " + offered.Key + "\n"
	return provider.Question{
		Finding: trust.Offer(),
		Prompt:  fmt.Sprintf("Trust that key and record %s in %s?", entry, store),
		Remedy:  trust.Remedy,
		Confirm: func(context.Context) error {
			if err := record(store, line); err != nil {
				return fmt.Errorf("record the host key in %s: %w", store, err)
			}
			return nil
		},
	}, nil
}

func HostTrustOf(err error) (HostTrust, bool) {
	var refused HostTrustRefusal
	if errors.As(err, &refused) {
		return refused.Trust, true
	}
	return HostTrust{}, false
}
