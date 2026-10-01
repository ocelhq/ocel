package envsource

import (
	"encoding/json"
	"fmt"
)

type Descriptor struct {
	kind    string
	options json.RawMessage
	decoded decodedOptions
}

func NewDescriptor(kind string, options json.RawMessage) (Descriptor, error) {
	return newDescriptor(kind, options, "a production or preview tier", func(k Kind) *Config { return k.Deployed })
}

func NewDevDescriptor(kind string, options json.RawMessage) (Descriptor, error) {
	return newDescriptor(kind, options, "ocel dev", func(k Kind) *Config { return k.Dev })
}

func newDescriptor(kind string, options json.RawMessage, reader string, configOf func(Kind) *Config) (Descriptor, error) {
	registered, known := kinds[kind]
	if !known {
		return Descriptor{}, fmt.Errorf("ocel knows no env source %q", kind)
	}
	config := configOf(registered)
	if config == nil {
		return Descriptor{}, fmt.Errorf("%q is no env source %s reads", kind, reader)
	}
	decoded, err := config.decode(options)
	if err != nil {
		return Descriptor{}, fmt.Errorf("the %s env source's options: %w", kind, err)
	}
	return Descriptor{kind: kind, options: options, decoded: decoded}, nil
}

func mustDescriptor(descriptor Descriptor, err error) Descriptor {
	if err != nil {
		panic(err)
	}
	return descriptor
}

func (d Descriptor) Kind() string { return d.kind }

func (d Descriptor) Options() json.RawMessage { return d.options }

func (d Descriptor) ID() string { return d.decoded.id }

func (d Descriptor) Reading() Reading {
	switch {
	case d.decoded.schedule != nil:
		return ReadingOnSchedule
	case d.decoded.open != nil:
		return ReadingWhereOcelRuns
	}
	return ReadingOwnStore
}

func (d Descriptor) CanCreate() bool { return d.decoded.canCreate }

func (d Descriptor) CanUpdate() bool { return d.decoded.canUpdate }

func (d Descriptor) CredentialVariables() []string { return d.decoded.credentialVariables }

func (d Descriptor) Open(dir string, lookupEnv func(string) (string, bool)) (Source, error) {
	if d.decoded.open == nil {
		return nil, fmt.Errorf("a %s env source is ocel's own to read, never a command or service to open", d.kind)
	}
	return d.decoded.open(dir, lookupEnv)
}

func (d Descriptor) RefuseLogin(login Login) error {
	if d.decoded.schedule == nil || d.decoded.schedule.refuseLogin == nil {
		return nil
	}
	return d.decoded.schedule.refuseLogin(login)
}

type storedDescriptor struct {
	Kind    string          `json:"kind"`
	Options json.RawMessage `json:"options,omitempty"`
}

func (d Descriptor) MarshalJSON() ([]byte, error) {
	return json.Marshal(storedDescriptor{Kind: d.kind, Options: d.options})
}

func (d *Descriptor) UnmarshalJSON(data []byte) error {
	var stored storedDescriptor
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}
	decoded, err := NewDescriptor(stored.Kind, stored.Options)
	if err != nil {
		return err
	}
	*d = decoded
	return nil
}

type Tiers struct {
	Production Descriptor
	Preview    Descriptor
	Dev        Descriptor
}

func DefaultTiers() Tiers {
	return Tiers{
		Production: mustDescriptor(NewDescriptor(Builtin, nil)),
		Preview:    mustDescriptor(NewDescriptor(Builtin, nil)),
		Dev:        mustDescriptor(NewDevDescriptor(Dotenv, nil)),
	}
}
