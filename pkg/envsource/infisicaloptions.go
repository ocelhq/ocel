package envsource

import (
	"fmt"
	"os/exec"
	"path"
	"regexp"
	"strings"
)

const (
	infisicalKind        = "infisical"
	infisicalCloud       = "https://app.infisical.com"
	infisicalTokenEnvVar = "INFISICAL_TOKEN"
)

type WritePolicy string

const (
	WriteNever   WritePolicy = "never"
	WriteMissing WritePolicy = "missing"
	WriteValues  WritePolicy = "values"
)

var _ = register(Kind{
	Name: infisicalKind,
	Deployed: &Config{
		Doc:     "Infisical, read as the machine identity auth names and synced every minute.",
		Options: InfisicalOptions{},
		decode:  decodeAs(decodeDeployedInfisical),
	},
	Dev: &Config{
		Doc:     "Infisical, read as you: from INFISICAL_TOKEN, or else the infisical CLI you are logged in to.",
		Options: InfisicalOptions{},
		decode:  decodeAs(decodeDevInfisical),
	},
})

type InfisicalOptions struct {
	Project     string         `json:"project" doc:"The id of the Infisical project the values live in."`
	Environment string         `json:"environment" doc:"The slug of the Infisical environment this tier reads, such as prod."`
	Path        string         `json:"path,omitempty" doc:"The Infisical folder the project's values sit under. A variables folder such as /web reads from that folder beneath it. Left off, the environment's root, /."`
	Host        string         `json:"host,omitempty" doc:"The base URL of a self-hosted Infisical. Left off, https://app.infisical.com."`
	Auth        *InfisicalAuth `json:"auth,omitempty" doc:"The machine identity production or preview reads as, keyed by its login method. Required there, and refused for dev, which reads as you."`
	Write       WritePolicy    `json:"write,omitempty" enum:"never,missing,values" doc:"What ocel may write into Infisical. \"missing\" creates a key a declaration names and Infisical lacks, and never overwrites one. \"values\" also updates a value Infisical holds, from ocel env set or the variables page, unless it changed in Infisical since ocel last read it. Nothing ocel does ever deletes a key there. Left off, \"never\". Refused for dev, which only reads."`
}

func (InfisicalOptions) Doc() string {
	return "The Infisical project and environment a tier reads its values from. Production and preview read it as the machine identity auth names; dev reads it as you."
}

type InfisicalAuth struct {
	Universal *UniversalAuth `json:"universal,omitempty" doc:"Universal Auth: a client id and client secret, each an ocel variable."`
	Identity  *IdentityAuth  `json:"identity,omitempty" doc:"This target's own cloud identity signs in, so no secret is stored."`
}

func (InfisicalAuth) Shorthands() []string { return nil }

func (InfisicalAuth) Doc() string { return "How ocel logs in to Infisical, keyed by the login method." }

type UniversalAuth struct {
	ClientID     Variable `json:"clientId" doc:"The ocel variable containing the Universal Auth client id."`
	ClientSecret Variable `json:"clientSecret" doc:"The ocel variable containing the Universal Auth client secret."`
}

func (UniversalAuth) Doc() string {
	return "Logs in to Infisical with a Universal Auth client id and client secret, each an ocel variable."
}

type IdentityAuth struct {
	IdentityID string `json:"identityId" doc:"The id of the Infisical machine identity to sign in as."`
}

func (IdentityAuth) Doc() string {
	return "Logs in to Infisical as the target's own cloud identity, so no secret is stored."
}

func (a *InfisicalAuth) Variables() []string {
	if a == nil || a.Universal == nil {
		return nil
	}
	return []string{a.Universal.ClientID.Name, a.Universal.ClientSecret.Name}
}

func (a *InfisicalAuth) method() string {
	if a.Identity != nil {
		return "identity"
	}
	return "universal"
}

func (o InfisicalOptions) Normalize() InfisicalOptions {
	o.Path = path.Clean("/" + o.Path)
	o.Host = strings.TrimRight(o.Host, "/")
	if o.Host == "" {
		o.Host = infisicalCloud
	}
	if o.Write == "" {
		o.Write = WriteNever
	}
	return o
}

func (o InfisicalOptions) ID() string {
	return infisicalKind + ":" + o.Project + "/" + o.Environment
}

func (o InfisicalOptions) secretPath(folder string) (string, error) {
	for segment := range strings.SplitSeq(folder, "/") {
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("folder %q climbs out of %s, and ocel reads and writes only beneath the path it was given", folder, o.Path)
		}
	}
	return path.Join(o.Path, "/"+strings.TrimPrefix(folder, "/")), nil
}

var (
	infisicalHost    = regexp.MustCompile(`^(https?://.+)?$`)
	variableNameText = regexp.MustCompile(`^[^#[:cntrl:]]*$`)
)

func refuseMalformedInfisical(options InfisicalOptions) error {
	if err := requireText("project", options.Project, "the id of the Infisical project this tier reads"); err != nil {
		return err
	}
	if err := requireText("environment", options.Environment, "the slug of the Infisical environment this tier reads"); err != nil {
		return err
	}
	if options.Path != "" && !strings.HasPrefix(options.Path, "/") {
		return &OptionError{Field: "path", Reason: "must start at the environment's root, /, such as /acme"}
	}
	if !infisicalHost.MatchString(options.Host) {
		return &OptionError{Field: "host", Reason: "must be the http or https URL of an Infisical"}
	}
	return nil
}

func decodeDevInfisical(options InfisicalOptions) (decodedOptions, error) {
	if err := refuseMalformedInfisical(options); err != nil {
		return decodedOptions{}, err
	}
	switch {
	case options.Auth != nil:
		return decodedOptions{}, &OptionError{Field: "auth", Reason: "is for production and preview: ocel dev reads Infisical as you, from INFISICAL_TOKEN or else the infisical CLI you are logged in to — drop it"}
	case options.Write != "":
		return decodedOptions{}, &OptionError{Field: "write", Reason: "is for production and preview: ocel dev only reads Infisical — drop it"}
	}
	options = options.Normalize()
	return decodedOptions{
		id: options.ID(),
		open: func(dir string, lookupEnv func(string) (string, bool)) (Source, error) {
			return openInfisicalAsDeveloper(options, dir, lookupEnv)
		},
	}, nil
}

func decodeDeployedInfisical(options InfisicalOptions) (decodedOptions, error) {
	if err := refuseMalformedInfisical(options); err != nil {
		return decodedOptions{}, err
	}
	if options.Auth == nil {
		return decodedOptions{}, &OptionError{Field: "auth", Reason: "is required: production and preview read Infisical as a machine identity, keyed by one of universal, identity"}
	}
	if options.Write != "" {
		if err := requireOneOf("write", options.Write, WriteNever, WriteMissing, WriteValues); err != nil {
			return decodedOptions{}, err
		}
	}
	if err := checkInfisicalAuth(options.Auth); err != nil {
		return decodedOptions{}, err
	}
	options = options.Normalize()
	location := []string{options.Host, options.Project, options.Environment, options.Path, options.Auth.method()}
	if options.Auth.Identity != nil {
		location = append(location, options.Auth.Identity.IdentityID)
	}
	return decodedOptions{
		id:                  options.ID(),
		canCreate:           options.Write == WriteMissing || options.Write == WriteValues,
		canUpdate:           options.Write == WriteValues,
		credentialVariables: options.Auth.Variables(),
		schedule: &schedule{
			location: strings.Join(location, dedupeKeySeparator),
			refuseLogin: func(login Login) error {
				if options.Auth.Identity == nil || login.ProveIdentity != nil {
					return nil
				}
				return fmt.Errorf("%s logs in with identity auth, which proves this target's cloud identity, and this target has none: log in with universal auth (clientId and clientSecret) instead", options.ID())
			},
			open: func(credentials []string, login Login) Source {
				return NewInfisical(options, infisicalCredential(options.Auth, credentials, login), login.Client)
			},
		},
	}, nil
}

func checkInfisicalAuth(auth *InfisicalAuth) error {
	if (auth.Universal == nil) == (auth.Identity == nil) {
		return &OptionError{Field: "auth", Reason: "must set exactly one of the keys universal, identity"}
	}
	if auth.Identity != nil {
		return requireText("auth.identity.identityId", auth.Identity.IdentityID, "the id of the Infisical machine identity to sign in as")
	}
	for _, field := range []struct {
		name     string
		variable Variable
	}{{"auth.universal.clientId", auth.Universal.ClientID}, {"auth.universal.clientSecret", auth.Universal.ClientSecret}} {
		if field.variable.Name == "" {
			return &OptionError{Field: field.name, Reason: "is required: the ocel variable containing it, written { \"$env\": \"NAME\" }"}
		}
		if !variableNameText.MatchString(field.variable.Name) {
			return &OptionError{Field: field.name, Reason: "must name an ocel variable with no # or control character in it"}
		}
	}
	return nil
}

func infisicalCredential(auth *InfisicalAuth, credentials []string, login Login) Credential {
	if auth.Identity != nil {
		return IdentityCredential(auth.Identity.IdentityID, login.ProveIdentity)
	}
	return UniversalCredential(credentials[0], credentials[1])
}

func openInfisicalAsDeveloper(options InfisicalOptions, dir string, lookupEnv func(string) (string, bool)) (Source, error) {
	if token, set := lookupEnv(infisicalTokenEnvVar); set && strings.TrimSpace(token) != "" {
		return NewInfisical(options, AccessToken(strings.TrimSpace(token)), nil), nil
	}
	cli, err := exec.LookPath(infisicalCLIName)
	if err != nil {
		return nil, fmt.Errorf("%s is read as you, and this shell offers no way in: export %s with an access token, or install the infisical CLI and run `infisical login`", options.ID(), infisicalTokenEnvVar)
	}
	return infisicalExport{options: options, cli: cli, dir: dir}, nil
}
