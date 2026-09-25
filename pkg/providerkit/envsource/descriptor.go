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

const InfisicalCloud = "https://app.infisical.com"

type WritePolicy string

const (
	WriteNever   WritePolicy = "never"
	WriteMissing WritePolicy = "missing"
)

type AuthMethod string

const (
	AuthUniversal AuthMethod = "universal"
	AuthAWS       AuthMethod = "aws"
	AuthGCP       AuthMethod = "gcp"
)

type Format string

const (
	FormatJSON   Format = "json"
	FormatDotenv Format = "dotenv"
)

const FolderPlaceholder = "{folder}"

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
	Method          AuthMethod `json:"method,omitempty"`
	ClientIDVar     string     `json:"clientIdVar,omitempty"`
	ClientSecretVar string     `json:"clientSecretVar,omitempty"`
	IdentityID      string     `json:"identityId,omitempty"`
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

func (o InfisicalOptions) Normalized() InfisicalOptions {
	o.Path = CleanPath(o.Path)
	o.Host = strings.TrimRight(o.Host, "/")
	if o.Host == "" {
		o.Host = InfisicalCloud
	}
	if o.Write == "" {
		o.Write = WriteNever
	}
	return o
}

func CleanPath(p string) string {
	if p == "" {
		return "/"
	}
	return path.Clean("/" + p)
}

func (d Descriptor) Standing() bool { return d.Kind == Infisical }

func (d Descriptor) Writable() bool {
	return d.Kind == Infisical && d.Infisical != nil && d.Infisical.Write == WriteMissing
}

func (a InfisicalAuth) Vars() []string {
	if a.Method != AuthUniversal {
		return nil
	}
	return []string{a.ClientIDVar, a.ClientSecretVar}
}

func (d Descriptor) ID() string {
	if d.Kind == Infisical && d.Infisical != nil {
		return InfisicalID(*d.Infisical)
	}
	return string(d.Kind)
}
