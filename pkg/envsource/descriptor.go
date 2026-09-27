package envsource

import (
	"fmt"
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
	WriteValues  WritePolicy = "values"
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

func (d Descriptor) ID() string {
	if d.Kind == Infisical && d.Infisical != nil {
		return d.Infisical.ID()
	}
	return string(d.Kind)
}

func (o InfisicalOptions) ID() string {
	return string(Infisical) + ":" + o.Project + "/" + o.Environment
}

func (o InfisicalOptions) secretPath(folder string) (string, error) {
	for segment := range strings.SplitSeq(folder, "/") {
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("folder %q climbs out of %s, and ocel reads and writes only beneath the path it was given", folder, o.Path)
		}
	}
	return path.Join(o.Path, "/"+strings.TrimPrefix(folder, "/")), nil
}

func (d Descriptor) CredentialVariables() []string {
	if d.Kind != Infisical || d.Infisical == nil {
		return nil
	}
	return d.Infisical.Auth.Variables()
}

func (a InfisicalAuth) Variables() []string {
	if a.Method != AuthUniversal {
		return nil
	}
	return []string{a.ClientIDVariable, a.ClientSecretVariable}
}
