package envsource

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

const (
	Builtin = "builtin"
	Dotenv  = "dotenv"
)

type Kind struct {
	Name     string
	Deployed *Config
	Dev      *Config
}

type Config struct {
	Doc     string
	Options any
	decode  func(options json.RawMessage) (decodedOptions, error)
}

type decodedOptions struct {
	id                  string
	canCreate           bool
	canUpdate           bool
	credentialVariables []string
	open                func(dir string, lookupEnv func(string) (string, bool)) (Source, error)
	schedule            *schedule
}

type schedule struct {
	location    string
	refuseLogin func(login Login) error
	open        func(credentials []string, login Login) Source
}

type Reading int

const (
	ReadingOwnStore Reading = iota
	ReadingWhereOcelRuns
	ReadingOnSchedule
)

type Variable struct {
	Name string `json:"$env"`
}

type OptionError struct {
	Field  string
	Reason string
}

func (e *OptionError) Error() string {
	if e.Field == "" {
		return e.Reason
	}
	return e.Field + " " + e.Reason
}

var kinds = map[string]Kind{}

func register(kind Kind) Kind {
	if _, taken := kinds[kind.Name]; taken {
		panic(fmt.Sprintf("two env source kinds are named %s", kind.Name))
	}
	kinds[kind.Name] = kind
	return kind
}

func Kinds() []Kind {
	out := make([]Kind, 0, len(kinds))
	for _, kind := range kinds {
		out = append(out, kind)
	}
	slices.SortFunc(out, func(a, b Kind) int { return strings.Compare(a.Name, b.Name) })
	return out
}

var _ = register(Kind{Name: Builtin, Deployed: &Config{decode: decodeOwnStore(Builtin)}})

var _ = register(Kind{Name: Dotenv, Dev: &Config{decode: decodeOwnStore(Dotenv)}})

func decodeOwnStore(kind string) func(json.RawMessage) (decodedOptions, error) {
	return func(options json.RawMessage) (decodedOptions, error) {
		switch string(bytes.TrimSpace(options)) {
		case "", "null", "{}":
			return decodedOptions{id: kind}, nil
		}
		return decodedOptions{}, &OptionError{Reason: fmt.Sprintf("%s takes no options", kind)}
	}
}

func decodeStrictly(options json.RawMessage, into any) error {
	decoder := json.NewDecoder(bytes.NewReader(options))
	decoder.DisallowUnknownFields()
	err := decoder.Decode(into)
	if err == nil {
		return nil
	}
	var mistyped *json.UnmarshalTypeError
	if errors.As(err, &mistyped) {
		return &OptionError{Field: mistyped.Field, Reason: "cannot be JSON " + mistyped.Value}
	}
	if field, unknown := strings.CutPrefix(err.Error(), "json: unknown field "); unknown {
		return &OptionError{Field: strings.Trim(field, `"`), Reason: "is not an option ocel knows"}
	}
	return &OptionError{Reason: "the options are not a JSON object: " + err.Error()}
}

func requireText(field, text, what string) error {
	if strings.TrimSpace(text) == "" {
		return &OptionError{Field: field, Reason: "is required: " + what}
	}
	return nil
}

func requireOneOf[T ~string](field string, value T, allowed ...T) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	names := make([]string, 0, len(allowed))
	for _, name := range allowed {
		names = append(names, string(name))
	}
	return &OptionError{Field: field, Reason: "must be one of " + strings.Join(names, ", ")}
}
