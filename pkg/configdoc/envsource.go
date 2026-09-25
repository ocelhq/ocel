package configdoc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

const (
	EnvSourceBuiltin   = "builtin"
	EnvSourceDotenv    = "dotenv"
	EnvSourceInfisical = "infisical"
	EnvSourceExec      = "exec"
)

var (
	envSourceKeyed   = []string{EnvSourceInfisical, EnvSourceExec}
	infisicalWrites  = []string{"missing", "never"}
	execFormats      = []string{"json", "dotenv"}
	infisicalAuthIDs = []string{"universal", "aws", "gcp"}
)

type EnvSourceConfig struct {
	Production *EnvSourceDescriptor    `json:"production,omitempty" doc:"Where production's values are read from. Left off, ocel's own store in your account (\"builtin\")."`
	Preview    *EnvSourceDescriptor    `json:"preview,omitempty" doc:"Where every preview's class-wide values are read from. Left off, ocel's own store in your account (\"builtin\"). A value set for one named preview stays ocel's to hold."`
	Dev        *DevEnvSourceDescriptor `json:"dev,omitempty" doc:"Where ocel dev and ocel run read values from on your machine. Left off, the project's .env file (\"dotenv\"). .env.local overrides whatever this reads."`
}

type EnvSourceDescriptor = envSourceSelector[deployedTier]

type DevEnvSourceDescriptor = envSourceSelector[devTier]

type envSourceSelector[T envSourceTier] struct {
	ID        string
	Infisical *InfisicalOptions
	Exec      *ExecOptions
}

type InfisicalOptions struct {
	Project     string         `json:"project" doc:"The id of the Infisical project the values live in."`
	Environment string         `json:"environment" doc:"The slug of the Infisical environment this tier reads, such as prod."`
	Path        string         `json:"path,omitempty" doc:"The Infisical folder the project's values sit under. A variables folder such as /web reads from that folder beneath it. Left off, the environment's root, /."`
	Host        string         `json:"host,omitempty" doc:"The base URL of a self-hosted Infisical. Left off, https://app.infisical.com."`
	Auth        *InfisicalAuth `json:"auth,omitempty" doc:"The machine identity a deployed tier reads as. Keyed by one login method. ocel dev never reads it: it signs in as you, from INFISICAL_TOKEN or the infisical CLI."`
	Write       string         `json:"write,omitempty" doc:"Whether ocel may write into Infisical. \"missing\" creates a key a declaration names and Infisical lacks, and never overwrites or deletes one. Left off, \"never\"." enum:"missing,never"`
}

type InfisicalAuth struct {
	Universal *UniversalAuth `json:"universal,omitempty" doc:"Universal Auth: a client id and client secret, each held as a value in ocel's own store."`
	AWS       *IdentityAuth  `json:"aws,omitempty" doc:"AWS IAM Auth: the target's own AWS role signs in, so no secret is stored."`
	GCP       *IdentityAuth  `json:"gcp,omitempty" doc:"GCP Auth: the target's own service account signs in, so no secret is stored."`
}

type UniversalAuth struct {
	ClientID     VarRef `json:"clientId" doc:"The value holding the Universal Auth client id."`
	ClientSecret VarRef `json:"clientSecret" doc:"The value holding the Universal Auth client secret."`
}

type IdentityAuth struct {
	IdentityID string `json:"identityId" doc:"The id of the Infisical machine identity to sign in as."`
}

type VarRef struct {
	Var string `json:"var" doc:"The name of a value ocel holds in its own store for this tier, set with ocel env set. It may reference another project's value."`
}

type ExecOptions struct {
	Command []string `json:"command" doc:"The command to run and its arguments. {folder} in an argument is replaced with the variables folder being read."`
	Format  string   `json:"format" doc:"What the command prints: a JSON object of names to values, or KEY=VALUE lines." enum:"json,dotenv"`
}

type envSourceTier interface {
	title() string
	alone() string
	named() string
	deployed() bool
}

type deployedTier struct{}

func (deployedTier) title() string  { return "EnvSourceDescriptor" }
func (deployedTier) alone() string  { return EnvSourceBuiltin }
func (deployedTier) named() string  { return "production and preview" }
func (deployedTier) deployed() bool { return true }

type devTier struct{}

func (devTier) title() string  { return "DevEnvSourceDescriptor" }
func (devTier) alone() string  { return EnvSourceDotenv }
func (devTier) named() string  { return "dev" }
func (devTier) deployed() bool { return false }

func (s *envSourceSelector[T]) UnmarshalJSON(data []byte) error {
	id, options, err := unmarshalSelector(data)
	if err != nil {
		return err
	}
	s.ID = id
	switch id {
	case EnvSourceInfisical:
		s.Infisical = &InfisicalOptions{}
		return json.Unmarshal(options, s.Infisical)
	case EnvSourceExec:
		s.Exec = &ExecOptions{}
		return json.Unmarshal(options, s.Exec)
	}
	return nil
}

func (envSourceSelector[T]) jsonSchema() object {
	var tier T
	return object{
		"title": tier.title(),
		"oneOf": []any{
			object{"type": "string", "enum": []any{tier.alone()}},
			object{
				"type":                 "object",
				"minProperties":        1,
				"maxProperties":        1,
				"additionalProperties": false,
				"properties": object{
					EnvSourceInfisical: schemaOf(reflect.TypeFor[InfisicalOptions]()),
					EnvSourceExec:      schemaOf(reflect.TypeFor[ExecOptions]()),
				},
			},
		},
	}
}

func (envSourceSelector[T]) checkShape(path string, value any) error {
	var tier T
	known := append([]string{tier.alone()}, envSourceKeyed...)
	listed := strings.Join(known, ", ")
	switch held := value.(type) {
	case string:
		return checkEnvSourceAlone(path, held, tier, listed)
	case map[string]any:
		keys := slices.Sorted(mapKeys(held))
		switch len(keys) {
		case 0:
			return fmt.Errorf("%s is keyed by nothing — name %q, or key it by one of %s", PathName(path), tier.alone(), strings.Join(envSourceKeyed, ", "))
		case 1:
		default:
			return fmt.Errorf("%s is keyed by %s, and a tier reads from one source — keep one of %s", PathName(path), strings.Join(keys, " and "), listed)
		}
		id := keys[0]
		if !slices.Contains(envSourceKeyed, id) {
			if slices.Contains(known, id) || id == EnvSourceBuiltin || id == EnvSourceDotenv {
				return checkEnvSourceAlone(path, id, tier, listed)
			}
			return fmt.Errorf("%s names %q, and ocel knows no such source — name one of %s", PathName(path), id, listed)
		}
		options, ok := held[id].(map[string]any)
		if !ok {
			return typeError(JoinPath(path, id), "an object of options")
		}
		at := JoinPath(path, id)
		if id == EnvSourceExec {
			return checkExec(at, options)
		}
		return checkInfisical(at, options, tier)
	default:
		return fmt.Errorf("%s must be %q, or an object keyed by one of %s", PathName(path), tier.alone(), strings.Join(envSourceKeyed, ", "))
	}
}

func checkEnvSourceAlone(path, id string, tier envSourceTier, listed string) error {
	switch id {
	case tier.alone():
		return nil
	case EnvSourceDotenv:
		return fmt.Errorf("%s names %q, which reads .env files on your machine and so serves dev alone — name %q, or key it by one of %s", PathName(path), id, EnvSourceBuiltin, strings.Join(envSourceKeyed, ", "))
	case EnvSourceBuiltin:
		return fmt.Errorf("%s names %q, ocel's own store in your account, which only production and preview deploy into — name %q, or key it by one of %s", PathName(path), id, EnvSourceDotenv, strings.Join(envSourceKeyed, ", "))
	case EnvSourceInfisical, EnvSourceExec:
		return fmt.Errorf("%s names %q with no options, and %s cannot go without them — write { %q: { … } }", PathName(path), id, id, id)
	}
	return fmt.Errorf("%s names %q, and ocel knows no such source — name one of %s", PathName(path), id, listed)
}

func checkInfisical(path string, options map[string]any, tier envSourceTier) error {
	if err := checkValue(path, reflect.TypeFor[InfisicalOptions](), options); err != nil {
		return err
	}
	for _, required := range []string{"project", "environment"} {
		if text, _ := options[required].(string); strings.TrimSpace(text) == "" {
			return fmt.Errorf("%s is required: the Infisical %s this tier reads", PathName(JoinPath(path, required)), required)
		}
	}
	if write, held := options["write"].(string); held && !slices.Contains(infisicalWrites, write) {
		return fmt.Errorf("%s must be one of %s", PathName(JoinPath(path, "write")), strings.Join(infisicalWrites, ", "))
	}
	_, authed := options["auth"]
	switch {
	case tier.deployed() && !authed:
		return fmt.Errorf("%s is required: a deployed tier reads Infisical as a machine identity, keyed by one of %s", PathName(JoinPath(path, "auth")), strings.Join(infisicalAuthIDs, ", "))
	case !tier.deployed() && authed:
		return fmt.Errorf("%s is for a deployed tier: ocel dev reads Infisical as you, from INFISICAL_TOKEN or the infisical CLI you are logged in to — drop it", PathName(JoinPath(path, "auth")))
	}
	return nil
}

func checkExec(path string, options map[string]any) error {
	if err := checkValue(path, reflect.TypeFor[ExecOptions](), options); err != nil {
		return err
	}
	command, _ := options["command"].([]any)
	if len(command) == 0 {
		return fmt.Errorf("%s is required: the command to run and its arguments, such as [\"op\", \"inject\"]", PathName(JoinPath(path, "command")))
	}
	if format, _ := options["format"].(string); !slices.Contains(execFormats, format) {
		return fmt.Errorf("%s must be one of %s", PathName(JoinPath(path, "format")), strings.Join(execFormats, ", "))
	}
	return nil
}

func (InfisicalAuth) checkShape(path string, value any) error {
	held, ok := value.(map[string]any)
	if !ok {
		return typeError(path, "an object keyed by one of "+strings.Join(infisicalAuthIDs, ", "))
	}
	keys := slices.Sorted(mapKeys(held))
	switch len(keys) {
	case 0:
		return fmt.Errorf("%s is keyed by nothing — key it by one of %s", PathName(path), strings.Join(infisicalAuthIDs, ", "))
	case 1:
	default:
		return fmt.Errorf("%s is keyed by %s, and Infisical is read as one identity — keep one of %s", PathName(path), strings.Join(keys, " and "), strings.Join(infisicalAuthIDs, ", "))
	}
	if err := checkObject(path, reflect.TypeFor[InfisicalAuth](), held); err != nil {
		return err
	}
	method, ok := held[keys[0]].(map[string]any)
	if !ok {
		return typeError(JoinPath(path, keys[0]), "an object")
	}
	if keys[0] == "universal" {
		for _, required := range []string{"clientId", "clientSecret"} {
			if _, set := method[required]; !set {
				return fmt.Errorf("%s is required", PathName(JoinPath(JoinPath(path, keys[0]), required)))
			}
		}
		return nil
	}
	if id, _ := method["identityId"].(string); strings.TrimSpace(id) == "" {
		return fmt.Errorf("%s is required: the Infisical machine identity to sign in as", PathName(JoinPath(JoinPath(path, keys[0]), "identityId")))
	}
	return nil
}

func (InfisicalAuth) jsonSchema() object {
	schema := objectSchema(reflect.TypeFor[InfisicalAuth]())
	schema["minProperties"] = 1
	schema["maxProperties"] = 1
	return schema
}

func (VarRef) checkShape(path string, value any) error {
	held, ok := value.(map[string]any)
	name, named := held["var"].(string)
	if !ok || !named || len(held) != 1 || strings.TrimSpace(name) == "" {
		return fmt.Errorf("%s must be { \"var\": \"NAME\" } — the name of a value ocel holds for this tier, never the secret itself", PathName(path))
	}
	return nil
}
