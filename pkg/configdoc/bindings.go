package configdoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
)

type Bindings map[string]map[string]Binding

type Binding struct {
	Published  string
	Production *InlineRecord
	Preview    *InlineRecord
}

type InlineRecord struct {
	Postgres *PostgresBinding
	Bucket   *BucketBinding
}

type Ref struct {
	Env string `json:"$env" doc:"The ocel variable holding this value, set per class and environment with ocel env set. The app never reads it as a variable of its own."`
}

type Text struct {
	Literal string
	Ref     *Ref
}

type PostgresTLS struct {
	Mode string `json:"mode" enum:"require,verify-full" doc:"require encrypts the connection; verify-full also checks the server's certificate and hostname."`
	CA   *Ref   `json:"ca,omitempty" doc:"The certificate authority the server's certificate chains to, as PEM, when it is not one the system trusts."`
}

type PostgresBinding struct {
	URL      *Ref         `json:"url,omitempty"`
	Host     *Text        `json:"host,omitempty"`
	Port     int          `json:"port,omitempty"`
	Database *Text        `json:"database,omitempty"`
	Username *Text        `json:"username,omitempty"`
	Password *Ref         `json:"password,omitempty"`
	TLS      *PostgresTLS `json:"tls,omitempty"`
}

type postgresURL struct {
	URL Ref `json:"url" doc:"The whole connection string, kept verbatim so its sslmode, options and pooler parameters reach the client."`
}

type postgresHost struct {
	Host     Text         `json:"host" doc:"The server's hostname."`
	Port     int          `json:"port,omitempty" doc:"The server's port. Left off, 5432."`
	Database Text         `json:"database" doc:"The database the app connects to."`
	Username Text         `json:"username" doc:"The role the app connects as."`
	Password Ref          `json:"password" doc:"The role's password."`
	TLS      *PostgresTLS `json:"tls,omitempty" doc:"How the connection is encrypted. Left off, each client library connects as it does by default."`
}

type BucketBinding struct {
	Endpoint        *Text `json:"endpoint,omitempty"`
	Region          *Text `json:"region,omitempty"`
	Bucket          *Text `json:"bucket,omitempty"`
	Prefix          *Text `json:"prefix,omitempty"`
	PathStyle       bool  `json:"pathStyle,omitempty"`
	AccessKeyID     *Ref  `json:"accessKeyId,omitempty"`
	SecretAccessKey *Ref  `json:"secretAccessKey,omitempty"`
	PublicBaseURL   *Text `json:"publicBaseUrl,omitempty"`
}

type bucketShape struct {
	Endpoint        Text  `json:"endpoint" doc:"The address of the S3-compatible store the bucket lives in, such as https://<account>.r2.cloudflarestorage.com."`
	Region          Text  `json:"region" doc:"The region requests to the store are signed for; auto for R2."`
	Bucket          Text  `json:"bucket" doc:"The bucket's name in that store."`
	Prefix          *Text `json:"prefix,omitempty" doc:"The prefix every key the app writes is kept under, so one bucket can hold several resources or environments."`
	PathStyle       bool  `json:"pathStyle,omitempty" doc:"Address the bucket as a path on the endpoint rather than as a subdomain of it, for stores that serve no virtual hosts."`
	AccessKeyID     Ref   `json:"accessKeyId" doc:"The public half of the key pair the runtime reaches the store with."`
	SecretAccessKey Ref   `json:"secretAccessKey" doc:"The secret half of that key pair."`
	PublicBaseURL   *Text `json:"publicBaseUrl,omitempty" doc:"The address the bucket's objects are served from publicly, when the code declares it public."`
}

const (
	TierProduction = "production"
	TierPreview    = "preview"
)

var tiers = []string{TierProduction, TierPreview}

type inlineForm struct {
	check  func(path string, value map[string]any) error
	schema func() object
	decode func(raw json.RawMessage) (*InlineRecord, error)
}

var inlineForms = map[string]inlineForm{
	"bucket": {
		check: func(path string, record map[string]any) error {
			return checkObject(path, reflect.TypeFor[bucketShape](), record)
		},
		schema: func() object { return titled(objectSchema(reflect.TypeFor[bucketShape]()), "BucketBinding") },
		decode: func(raw json.RawMessage) (*InlineRecord, error) {
			record := &BucketBinding{}
			if err := json.Unmarshal(raw, record); err != nil {
				return nil, err
			}
			return &InlineRecord{Bucket: record}, nil
		},
	},
	"postgres": {
		check:  checkPostgres,
		schema: PostgresBinding{}.jsonSchema,
		decode: func(raw json.RawMessage) (*InlineRecord, error) {
			record := &PostgresBinding{}
			if err := json.Unmarshal(raw, record); err != nil {
				return nil, err
			}
			return &InlineRecord{Postgres: record}, nil
		},
	},
}

func BindableTypes() []string {
	out := make([]string, 0, 4)
	for _, typ := range naming.BindableResourceTypes() {
		out = append(out, naming.ResourceTypeName(typ))
	}
	slices.Sort(out)
	return out
}

func (b *Bindings) UnmarshalJSON(data []byte) error {
	var raw map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(Bindings, len(raw))
	for kind, named := range raw {
		out[kind] = make(map[string]Binding, len(named))
		for declared, value := range named {
			binding, err := decodeBinding(kind, value)
			if err != nil {
				return fmt.Errorf("bindings.%s.%s: %w", kind, declared, err)
			}
			out[kind][declared] = binding
		}
	}
	*b = out
	return nil
}

func decodeBinding(kind string, raw json.RawMessage) (Binding, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var published string
		err := json.Unmarshal(trimmed, &published)
		return Binding{Published: published}, err
	}
	form, ok := inlineForms[kind]
	if !ok {
		return Binding{}, fmt.Errorf("a %s binding is written \"@<name>\"", kind)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &object); err != nil {
		return Binding{}, err
	}
	if !tiered(keysOf(object)) {
		record, err := form.decode(trimmed)
		return Binding{Production: record, Preview: record}, err
	}
	var binding Binding
	for tier, body := range object {
		record, err := form.decode(body)
		if err != nil {
			return Binding{}, err
		}
		switch tier {
		case TierProduction:
			binding.Production = record
		case TierPreview:
			binding.Preview = record
		}
	}
	return binding, nil
}

func keysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func tiered(keys []string) bool {
	return len(keys) > 0 && slices.ContainsFunc(keys, func(key string) bool { return slices.Contains(tiers, key) })
}

func (Bindings) checkShape(path string, value any) error {
	object, ok := value.(map[string]any)
	if !ok {
		return typeError(path, "an object")
	}
	known := BindableTypes()
	for _, key := range keysOf(object) {
		if !slices.Contains(known, key) {
			return unknownKeyError(path, key, known)
		}
		named, ok := object[key].(map[string]any)
		if !ok {
			return typeError(JoinPath(path, key), "an object of declared name to binding")
		}
		for _, declared := range keysOf(named) {
			if err := checkBinding(JoinPath(JoinPath(path, key), declared), key, named[declared]); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkBinding(path, kind string, value any) error {
	form, inline := inlineForms[kind]
	switch held := value.(type) {
	case string:
		return nil
	case map[string]any:
		if !inline {
			return fmt.Errorf("%s must be \"@<name>\": a %s is bound to a record published elsewhere", PathName(path), kind)
		}
		keys := keysOf(held)
		if !tiered(keys) {
			return form.check(path, held)
		}
		for _, tier := range keys {
			if !slices.Contains(tiers, tier) {
				return UnknownKeyError{Path: JoinPath(path, tier), Known: tiers}
			}
			record, ok := held[tier].(map[string]any)
			if !ok {
				return typeError(JoinPath(path, tier), fmt.Sprintf("the %s record %s binds", kind, tier))
			}
			if err := form.check(JoinPath(path, tier), record); err != nil {
				return err
			}
		}
		return nil
	default:
		if !inline {
			return typeError(path, "\"@\" followed by the name the record is published under")
		}
		return typeError(path, fmt.Sprintf("\"@\" followed by the name the record is published under, or the %s record itself", kind))
	}
}

func checkPostgres(path string, record map[string]any) error {
	if _, byURL := record["url"]; byURL {
		if others := slices.DeleteFunc(keysOf(record), func(key string) bool { return key == "url" }); len(others) > 0 {
			return fmt.Errorf("%s sets url and %s: url is the whole connection, so it stands alone — drop url, or drop %s", PathName(path), strings.Join(others, ", "), strings.Join(others, ", "))
		}
		return checkObject(path, reflect.TypeFor[postgresURL](), record)
	}
	if err := checkObject(path, reflect.TypeFor[postgresHost](), record); err != nil {
		return err
	}
	tls, _ := record["tls"].(map[string]any)
	if mode, set := tls["mode"]; set && !slices.Contains(tlsModes, fmt.Sprint(mode)) {
		return fmt.Errorf("%s is %v, and a bound postgres connection is encrypted as %s", PathName(JoinPath(JoinPath(path, "tls"), "mode")), mode, strings.Join(tlsModes, " or "))
	}
	return nil
}

var tlsModes = []string{"require", "verify-full"}

func (PostgresBinding) jsonSchema() object {
	return object{
		"title": "PostgresBinding",
		"oneOf": []any{
			titled(objectSchema(reflect.TypeFor[postgresURL]()), "PostgresUrlBinding"),
			titled(objectSchema(reflect.TypeFor[postgresHost]()), "PostgresHostBinding"),
		},
	}
}

func titled(schema object, title string) object {
	schema["title"] = title
	return schema
}

func (Ref) checkShape(path string, value any) error {
	object, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("%s is a secret, so it takes an ocel variable, written { \"$env\": \"NAME\" }, and never text", PathName(path))
	}
	return checkObject(path, reflect.TypeFor[refShape](), object)
}

type refShape struct {
	Env string `json:"$env"`
}

func (Ref) jsonSchema() object {
	schema := titled(objectSchema(reflect.TypeFor[Ref]()), "VariableRef")
	schema["properties"].(object)["$env"].(object)["pattern"] = `^[A-Z_][A-Z0-9_]*$`
	return schema
}

func (Text) checkShape(path string, value any) error {
	if _, spelled := value.(string); spelled {
		return nil
	}
	if _, ok := value.(map[string]any); !ok {
		return typeError(path, "text, or an ocel variable written { \"$env\": \"NAME\" }")
	}
	return Ref{}.checkShape(path, value)
}

func (Text) jsonSchema() object {
	return object{"oneOf": []any{object{"type": "string"}, Ref{}.jsonSchema()}}
}

func (t *Text) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		return json.Unmarshal(trimmed, &t.Literal)
	}
	t.Ref = &Ref{}
	return json.Unmarshal(trimmed, t.Ref)
}

func (Bindings) jsonSchema() object {
	properties := object{}
	for _, name := range BindableTypes() {
		published := object{"type": "string", "pattern": `^@\S`}
		form, inline := inlineForms[name]
		if !inline {
			properties[name] = object{
				"type":                 "object",
				"additionalProperties": published,
				"description": fmt.Sprintf(
					"Each key is a %s resource this project declares; its value is \"@\" followed by the name the record is published under, such as \"@warehouse\".",
					name),
			}
			continue
		}
		tier := func(description string) object {
			record := form.schema()
			record["description"] = description
			return record
		}
		properties[name] = object{
			"type": "object",
			"additionalProperties": object{"oneOf": []any{
				published,
				form.schema(),
				object{
					"type":          "object",
					"minProperties": 1,
					"properties": object{
						TierProduction: tier("The record production binds. Left off, production provisions its own."),
						TierPreview:    tier("The record previews bind. Left off, previews provision their own."),
					},
					"additionalProperties": false,
				},
			}},
			"description": fmt.Sprintf(
				"Each key is a %s resource this project declares. Its value is \"@\" followed by the name a record is published under, such as \"@warehouse\"; or the record itself, whose secrets are ocel variables written { \"$env\": \"NAME\" }; or that record keyed by the tier it serves, leaving the other tier to provision its own.",
				name),
		}
	}
	return object{"type": "object", "properties": properties, "additionalProperties": false}
}
