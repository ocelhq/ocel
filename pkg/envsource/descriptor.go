package envsource

import (
	"path"
	"strings"
)

type Kind string

const (
	Builtin   Kind = "builtin"
	Dotenv    Kind = "dotenv"
	Infisical Kind = "infisical"
	Exec      Kind = "exec"
)

const infisicalCloud = "https://app.infisical.com"

type WritePolicy string

const (
	WriteNever   WritePolicy = "never"
	WriteMissing WritePolicy = "missing"
)

type AuthMethod string

const (
	AuthUniversal AuthMethod = "universal"
	AuthIdentity  AuthMethod = "identity"
)

type Format string

const (
	FormatJSON   Format = "json"
	FormatDotenv Format = "dotenv"
)

type Descriptor struct {
	Kind      Kind              `json:"kind"`
	Infisical *InfisicalOptions `json:"infisical,omitempty"`
	Exec      *ExecOptions      `json:"exec,omitempty"`
}

type InfisicalOptions struct {
	Project     string        `json:"project"`
	Environment string        `json:"environment"`
	Path        string        `json:"path"`
	Host        string        `json:"host"`
	Auth        InfisicalAuth `json:"auth"`
	Write       WritePolicy   `json:"write"`
}

type InfisicalAuth struct {
	Method               AuthMethod `json:"method,omitempty"`
	ClientIDVariable     string     `json:"clientIdVariable,omitempty"`
	ClientSecretVariable string     `json:"clientSecretVariable,omitempty"`
	IdentityID           string     `json:"identityId,omitempty"`
}

type ExecOptions struct {
	Command []string `json:"command"`
	Format  Format   `json:"format"`
}

type Tiers struct {
	Production Descriptor
	Preview    Descriptor
	Dev        Descriptor
}

func DefaultTiers() Tiers {
	return Tiers{
		Production: Descriptor{Kind: Builtin},
		Preview:    Descriptor{Kind: Builtin},
		Dev:        Descriptor{Kind: Dotenv},
	}
}

func (o InfisicalOptions) Normalize() InfisicalOptions {
	o.Path = cleanPath(o.Path)
	o.Host = strings.TrimRight(o.Host, "/")
	if o.Host == "" {
		o.Host = infisicalCloud
	}
	if o.Write == "" {
		o.Write = WriteNever
	}
	return o
}

func cleanPath(p string) string {
	return path.Clean("/" + p)
}

func (d Descriptor) IsScheduled() bool { return d.Kind == Infisical }

func (d Descriptor) CanWrite() bool {
	return d.Kind == Infisical && d.Infisical != nil && d.Infisical.Write == WriteMissing
}

func (a InfisicalAuth) Variables() []string {
	if a.Method != AuthUniversal {
		return nil
	}
	return []string{a.ClientIDVariable, a.ClientSecretVariable}
}
