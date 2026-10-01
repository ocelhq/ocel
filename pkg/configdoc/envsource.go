package configdoc

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/envsource"
)

type envSourceTier struct {
	configOf func(envsource.Kind) *envsource.Config
	decode   func(kind string, options json.RawMessage) (envsource.Descriptor, error)
}

var (
	deployedTier = envSourceTier{
		configOf: func(kind envsource.Kind) *envsource.Config { return kind.Deployed },
		decode:   envsource.NewDescriptor,
	}
	devTier = envSourceTier{
		configOf: func(kind envsource.Kind) *envsource.Config { return kind.Dev },
		decode:   envsource.NewDevDescriptor,
	}
)

func (t envSourceTier) namedAlone() []string {
	var out []string
	for _, kind := range envsource.Kinds() {
		if config := t.configOf(kind); config != nil && config.Options == nil {
			out = append(out, kind.Name)
		}
	}
	return out
}

func (t envSourceTier) keyed() []envsource.Kind {
	var out []envsource.Kind
	for _, kind := range envsource.Kinds() {
		if config := t.configOf(kind); config != nil && config.Options != nil {
			out = append(out, kind)
		}
	}
	return out
}

func (t envSourceTier) keys() []string {
	var out []string
	for _, kind := range t.keyed() {
		out = append(out, kind.Name)
	}
	return out
}

func (t envSourceTier) optionsOf(id string) (*envsource.Config, bool) {
	for _, kind := range t.keyed() {
		if kind.Name == id {
			return t.configOf(kind), true
		}
	}
	return nil, false
}

func (t envSourceTier) unmarshal(data []byte) (envsource.Descriptor, error) {
	var spelled string
	if err := json.Unmarshal(data, &spelled); err == nil {
		return t.decode(spelled, nil)
	}
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(data, &keyed); err != nil {
		return envsource.Descriptor{}, err
	}
	if len(keyed) != 1 {
		return envsource.Descriptor{}, errors.New("an env source is keyed by exactly one identifier")
	}
	for id, options := range keyed {
		return t.decode(id, options)
	}
	return envsource.Descriptor{}, nil
}

func (t envSourceTier) schema(target reflect.Type) object {
	var alternatives []any
	if alone := t.namedAlone(); len(alone) > 0 {
		alternatives = append(alternatives, object{"type": "string", "enum": toAny(alone)})
	}
	for _, kind := range t.keyed() {
		config := t.configOf(kind)
		alternatives = append(alternatives, object{
			"type":                 "object",
			"properties":           object{kind.Name: describedAs(schemaOf(reflect.TypeOf(config.Options)), config.Doc)},
			"required":             []any{kind.Name},
			"additionalProperties": false,
		})
	}
	return named(target, object{"oneOf": alternatives})
}

var variableType = reflect.TypeFor[envsource.Variable]()

func spelledAs(target reflect.Type) reflect.Type {
	if target == variableType {
		return reflect.TypeFor[Ref]()
	}
	return target
}

type EnvSourceConfig struct {
	Production *EnvSourceDescriptor    `json:"production,omitempty" doc:"Where production's values are read from. Left off, ocel's own store in your account (\"builtin\")."`
	Preview    *EnvSourceDescriptor    `json:"preview,omitempty" doc:"Where every preview's tier-wide values are read from. Left off, ocel's own store in your account (\"builtin\"). A value set for one named preview stays ocel's own."`
	Dev        *DevEnvSourceDescriptor `json:"dev,omitempty" doc:"Where ocel dev and ocel run read values from on your machine. Left off, the project's .env file (\"dotenv\"). .env.local overrides whatever this reads."`
}

func (c *EnvSourceConfig) Tiers() envsource.Tiers {
	tiers := envsource.DefaultTiers()
	if c == nil {
		return tiers
	}
	if c.Production != nil {
		tiers.Production = c.Production.Descriptor
	}
	if c.Preview != nil {
		tiers.Preview = c.Preview.Descriptor
	}
	if c.Dev != nil {
		tiers.Dev = c.Dev.Descriptor
	}
	return tiers
}

type EnvSourceDescriptor struct {
	Descriptor envsource.Descriptor
}

func (EnvSourceDescriptor) Doc() string {
	return "Where production or preview reads its values from: ocel's own store in your account (\"builtin\"), or an env source keyed by its identifier with its options as the value."
}

func (d *EnvSourceDescriptor) UnmarshalJSON(data []byte) (err error) {
	d.Descriptor, err = deployedTier.unmarshal(data)
	return err
}

func (EnvSourceDescriptor) checkShape(path string, value any) error {
	return checkEnvSource(path, value, deployedTier)
}

func (EnvSourceDescriptor) jsonSchema() object {
	return deployedTier.schema(reflect.TypeFor[EnvSourceDescriptor]())
}

type DevEnvSourceDescriptor struct {
	Descriptor envsource.Descriptor
}

func (DevEnvSourceDescriptor) Doc() string {
	return "Where ocel dev and ocel run read values from on your machine: the project's .env file (\"dotenv\"), or an env source keyed by its identifier with its options as the value."
}

func (d *DevEnvSourceDescriptor) UnmarshalJSON(data []byte) (err error) {
	d.Descriptor, err = devTier.unmarshal(data)
	return err
}

func (DevEnvSourceDescriptor) checkShape(path string, value any) error {
	return checkEnvSource(path, value, devTier)
}

func (DevEnvSourceDescriptor) jsonSchema() object {
	return devTier.schema(reflect.TypeFor[DevEnvSourceDescriptor]())
}

func checkEnvSource(path string, value any, tier envSourceTier) error {
	keys := strings.Join(tier.keys(), ", ")
	alone := strings.Join(tier.namedAlone(), ", ")
	switch typed := value.(type) {
	case string:
		return checkEnvSourceNamedAlone(path, typed, tier)
	case map[string]any:
		named := keysOf(typed)
		switch len(named) {
		case 0:
			return fmt.Errorf("%s is keyed by nothing — name %q, or key it by one of %s", PathName(path), alone, keys)
		case 1:
		default:
			return fmt.Errorf("%s is keyed by %s, and a tier reads from one env source — keep one of %s", PathName(path), strings.Join(named, " and "), keys)
		}
		id := named[0]
		config, keyed := tier.optionsOf(id)
		if !keyed {
			if err := checkEnvSourceNamedAlone(path, id, tier); err != nil {
				return err
			}
			return fmt.Errorf("%s is keyed by %q, which takes no options — write it named alone, as %q", PathName(path), id, id)
		}
		at := JoinPath(path, id)
		options, ok := typed[id].(map[string]any)
		if !ok {
			return typeError(at, "an object of options")
		}
		if err := checkValue(at, reflect.TypeOf(config.Options), options); err != nil {
			return err
		}
		raw, err := json.Marshal(options)
		if err != nil {
			return err
		}
		if _, err := tier.decode(id, raw); err != nil {
			return optionError(at, err)
		}
		return nil
	default:
		return fmt.Errorf("%s must be %q, or an object keyed by one of %s", PathName(path), alone, keys)
	}
}

func optionError(path string, err error) error {
	var refused *envsource.OptionError
	if !errors.As(err, &refused) {
		return fmt.Errorf("%s: %w", PathName(path), err)
	}
	if refused.Field == "" {
		return fmt.Errorf("%s %s", PathName(path), refused.Reason)
	}
	return fmt.Errorf("%s %s", PathName(JoinPath(path, refused.Field)), refused.Reason)
}

func checkEnvSourceNamedAlone(path, id string, tier envSourceTier) error {
	keys := strings.Join(tier.keys(), ", ")
	alone := strings.Join(tier.namedAlone(), ", ")
	_, keyed := tier.optionsOf(id)
	switch {
	case slices.Contains(tier.namedAlone(), id):
		return nil
	case id == envsource.Dotenv:
		return fmt.Errorf("%s names %q, which reads .env files on your machine and so serves dev alone — name %q, or key it by one of %s", PathName(path), id, envsource.Builtin, keys)
	case id == envsource.Builtin:
		return fmt.Errorf("%s names %q, ocel's own store in your account, which only production and preview deploy into — name %q, or key it by one of %s", PathName(path), id, envsource.Dotenv, keys)
	case keyed:
		return fmt.Errorf("%s names %q with no options, and %s cannot go without them — write { %q: { … } }", PathName(path), id, id, id)
	}
	return fmt.Errorf("%s names %q, and ocel knows no such env source — name one of %s, %s", PathName(path), id, alone, keys)
}
