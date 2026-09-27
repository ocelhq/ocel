package configdoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

const (
	envSourceBuiltin   = "builtin"
	envSourceDotenv    = "dotenv"
	envSourceInfisical = "infisical"
	envSourceExec      = "exec"
)

var envSourceKeys = []string{envSourceInfisical, envSourceExec}

type envSourceTier struct {
	alone    string
	deployed bool
}

var (
	deployedTier = envSourceTier{alone: envSourceBuiltin, deployed: true}
	devTier      = envSourceTier{alone: envSourceDotenv}
)

type EnvSourceConfig struct {
	Production *EnvSourceDescriptor    `json:"production,omitempty" doc:"Where production's values are read from. Left off, ocel's own store in your account (\"builtin\")."`
	Preview    *EnvSourceDescriptor    `json:"preview,omitempty" doc:"Where every preview's class-wide values are read from. Left off, ocel's own store in your account (\"builtin\"). A value set for one named preview stays ocel's own."`
	Dev        *DevEnvSourceDescriptor `json:"dev,omitempty" doc:"Where ocel dev and ocel run read values from on your machine. Left off, the project's .env file (\"dotenv\"). .env.local overrides whatever this reads."`
}

type EnvSourceDescriptor struct {
	Infisical *InfisicalOptions `json:"infisical,omitempty" doc:"Infisical, read as the machine identity auth names and synced every minute."`
	Exec      *ExecOptions      `json:"exec,omitempty" doc:"A command run on the machine that deploys, whose output is the tier's values. It runs at each deploy and on no schedule."`
}

func (EnvSourceDescriptor) Shorthands() []string { return []string{envSourceBuiltin} }

func (d *EnvSourceDescriptor) UnmarshalJSON(data []byte) error {
	type keyed EnvSourceDescriptor
	return unmarshalEnvSource(data, (*keyed)(d))
}

type DevEnvSourceDescriptor struct {
	Infisical *InfisicalOptions `json:"infisical,omitempty" doc:"Infisical, read as you: from INFISICAL_TOKEN, or else the infisical CLI you are logged in to."`
	Exec      *ExecOptions      `json:"exec,omitempty" doc:"A command run on your machine, whose output is the values."`
}

func (DevEnvSourceDescriptor) Shorthands() []string { return []string{envSourceDotenv} }

func (d *DevEnvSourceDescriptor) UnmarshalJSON(data []byte) error {
	type keyed DevEnvSourceDescriptor
	return unmarshalEnvSource(data, (*keyed)(d))
}

func unmarshalEnvSource(data []byte, into any) error {
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && trimmed[0] == '"' {
		return nil
	}
	return json.Unmarshal(data, into)
}

func (EnvSourceDescriptor) checkShape(path string, value any) error {
	return checkEnvSource(path, value, deployedTier)
}

func (DevEnvSourceDescriptor) checkShape(path string, value any) error {
	return checkEnvSource(path, value, devTier)
}

func checkEnvSource(path string, value any, tier envSourceTier) error {
	keys := strings.Join(envSourceKeys, ", ")
	switch typed := value.(type) {
	case string:
		return checkEnvSourceNamedAlone(path, typed, tier)
	case map[string]any:
		named := keysOf(typed)
		switch len(named) {
		case 0:
			return fmt.Errorf("%s is keyed by nothing — name %q, or key it by one of %s", PathName(path), tier.alone, keys)
		case 1:
		default:
			return fmt.Errorf("%s is keyed by %s, and a tier reads from one env source — keep one of %s", PathName(path), strings.Join(named, " and "), keys)
		}
		id := named[0]
		if !slices.Contains(envSourceKeys, id) {
			if err := checkEnvSourceNamedAlone(path, id, tier); err != nil {
				return err
			}
			return fmt.Errorf("%s is keyed by %q, which takes no options — write it named alone, as %q", PathName(path), id, id)
		}
		options, ok := typed[id].(map[string]any)
		if !ok {
			return typeError(JoinPath(path, id), "an object of options")
		}
		if id == envSourceExec {
			return checkExec(JoinPath(path, id), options)
		}
		return checkInfisical(JoinPath(path, id), options, tier)
	default:
		return fmt.Errorf("%s must be %q, or an object keyed by one of %s", PathName(path), tier.alone, keys)
	}
}

func checkEnvSourceNamedAlone(path, id string, tier envSourceTier) error {
	keys := strings.Join(envSourceKeys, ", ")
	switch id {
	case tier.alone:
		return nil
	case envSourceDotenv:
		return fmt.Errorf("%s names %q, which reads .env files on your machine and so serves dev alone — name %q, or key it by one of %s", PathName(path), id, envSourceBuiltin, keys)
	case envSourceBuiltin:
		return fmt.Errorf("%s names %q, ocel's own store in your account, which only production and preview deploy into — name %q, or key it by one of %s", PathName(path), id, envSourceDotenv, keys)
	case envSourceInfisical, envSourceExec:
		return fmt.Errorf("%s names %q with no options, and %s cannot go without them — write { %q: { … } }", PathName(path), id, id, id)
	}
	return fmt.Errorf("%s names %q, and ocel knows no such env source — name one of %s, %s", PathName(path), id, tier.alone, keys)
}

func checkInfisical(path string, options map[string]any, tier envSourceTier) error {
	target := reflect.TypeFor[InfisicalOptions]()
	if err := checkValue(path, target, options); err != nil {
		return err
	}
	if err := requireText(path, options, "project", "the id of the Infisical project this tier reads"); err != nil {
		return err
	}
	if err := requireText(path, options, "environment", "the slug of the Infisical environment this tier reads"); err != nil {
		return err
	}
	_, writes := options["write"]
	auth, authed := options["auth"].(map[string]any)
	switch {
	case !tier.deployed && authed:
		return fmt.Errorf("%s is for production and preview: ocel dev reads Infisical as you, from INFISICAL_TOKEN or else the infisical CLI you are logged in to — drop it", PathName(JoinPath(path, "auth")))
	case !tier.deployed && writes:
		return fmt.Errorf("%s is for production and preview: ocel dev only reads Infisical — drop it", PathName(JoinPath(path, "write")))
	case tier.deployed && !authed:
		return fmt.Errorf("%s is required: production and preview read Infisical as a machine identity, keyed by one of %s", PathName(JoinPath(path, "auth")), strings.Join(KeysOf(InfisicalAuth{}), ", "))
	}
	if err := checkEnum(path, target, options, "write"); err != nil {
		return err
	}
	if !authed {
		return nil
	}
	return checkInfisicalAuth(JoinPath(path, "auth"), auth)
}

func checkInfisicalAuth(path string, auth map[string]any) error {
	method := keysOf(auth)[0]
	options, _ := auth[method].(map[string]any)
	at := JoinPath(path, method)
	if method == "identity" {
		return requireText(at, options, "identityId", "the id of the Infisical machine identity to sign in as")
	}
	for _, key := range []string{"clientId", "clientSecret"} {
		if _, set := options[key]; !set {
			return fmt.Errorf("%s is required: the ocel variable containing it, written { \"$env\": \"NAME\" }", PathName(JoinPath(at, key)))
		}
	}
	return nil
}

func checkExec(path string, options map[string]any) error {
	target := reflect.TypeFor[ExecOptions]()
	if err := checkValue(path, target, options); err != nil {
		return err
	}
	if command, _ := options["command"].([]any); len(command) == 0 {
		return fmt.Errorf("%s is required: the command to run and its arguments, such as [\"op\", \"inject\"]", PathName(JoinPath(path, "command")))
	}
	if _, set := options["format"]; !set {
		return fmt.Errorf("%s is required: one of %s", PathName(JoinPath(path, "format")), strings.Join(enumOf(target, "format"), ", "))
	}
	return checkEnum(path, target, options, "format")
}

func requireText(path string, object map[string]any, key, what string) error {
	if text, _ := object[key].(string); strings.TrimSpace(text) == "" {
		return fmt.Errorf("%s is required: %s", PathName(JoinPath(path, key)), what)
	}
	return nil
}

func checkEnum(path string, target reflect.Type, object map[string]any, key string) error {
	value, set := object[key].(string)
	allowed := enumOf(target, key)
	if !set || slices.Contains(allowed, value) {
		return nil
	}
	return fmt.Errorf("%s must be one of %s", PathName(JoinPath(path, key)), strings.Join(allowed, ", "))
}

func enumOf(target reflect.Type, key string) []string {
	for _, field := range jsonFields(target) {
		if field.name == key {
			return field.enum
		}
	}
	return nil
}

type InfisicalOptions struct {
	Project     string         `json:"project" doc:"The id of the Infisical project the values live in."`
	Environment string         `json:"environment" doc:"The slug of the Infisical environment this tier reads, such as prod."`
	Path        string         `json:"path,omitempty" doc:"The Infisical folder the project's values sit under. A variables folder such as /web reads from that folder beneath it. Left off, the environment's root, /."`
	Host        string         `json:"host,omitempty" doc:"The base URL of a self-hosted Infisical. Left off, https://app.infisical.com."`
	Auth        *InfisicalAuth `json:"auth,omitempty" doc:"The machine identity production or preview reads as, keyed by its login method. Required there, and refused for dev, which reads as you."`
	Write       string         `json:"write,omitempty" enum:"missing,never" doc:"Whether a deploy may write into Infisical. \"missing\" creates a key a declaration names and Infisical lacks, and never overwrites or deletes one. Left off, \"never\". Refused for dev, which only reads."`
}

type InfisicalAuth struct {
	Universal *UniversalAuth `json:"universal,omitempty" doc:"Universal Auth: a client id and client secret, each an ocel variable."`
	Identity  *IdentityAuth  `json:"identity,omitempty" doc:"This target's own cloud identity signs in, so no secret is stored."`
}

func (InfisicalAuth) Shorthands() []string { return nil }

type UniversalAuth struct {
	ClientID     Ref `json:"clientId" doc:"The ocel variable containing the Universal Auth client id."`
	ClientSecret Ref `json:"clientSecret" doc:"The ocel variable containing the Universal Auth client secret."`
}

type IdentityAuth struct {
	IdentityID string `json:"identityId" doc:"The id of the Infisical machine identity to sign in as."`
}

type ExecOptions struct {
	Command []string `json:"command" doc:"The command to run and its arguments. {folder} in an argument is replaced with the variables folder being read."`
	Format  string   `json:"format" enum:"json,dotenv" doc:"What the command prints: a JSON object of names to values, or KEY=VALUE lines."`
}
