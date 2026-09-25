package projectconfig

import (
	"cmp"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/ocelhq/ocel/pkg/configdoc"
	"github.com/ocelhq/ocel/pkg/naming"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

type Binding struct {
	Type     resourcesv1.ResourceType
	Name     string
	Tier     environmentv1.Tier
	External string
	Inline   *Inline
}

type Inline struct {
	Postgres *PostgresInline
	Bucket   *BucketInline
}

type BucketInline struct {
	Endpoint        Value
	Region          Value
	Bucket          Value
	Prefix          Value
	PathStyle       bool
	AccessKeyID     string
	SecretAccessKey string
	PublicBaseURL   Value
}

type Value struct {
	Literal  string
	Variable string
}

func (v Value) Resolve(read func(variable string) (string, error)) (string, error) {
	if v.Variable == "" {
		return v.Literal, nil
	}
	return read(v.Variable)
}

type PostgresInline struct {
	URL      string
	Host     Value
	Port     int
	Database Value
	Username Value
	Password string
	TLS      *PostgresTLS
}

type PostgresTLS struct {
	Mode string
	CA   string
}

func (c *Config) BindingsFor(tier environmentv1.Tier) []Binding {
	out := make([]Binding, 0, len(c.Bindings))
	for _, b := range c.Bindings {
		if b.Tier == environmentv1.Tier_TIER_UNSPECIFIED || b.Tier == tier {
			out = append(out, b)
		}
	}
	return out
}

func (b Binding) Group() string {
	return naming.ResourceTypeName(b.Type) + "." + b.Name
}

func (b Binding) RecordName() string {
	if b.Inline != nil {
		return naming.InlineRecordName(b.Type, b.Name)
	}
	return b.External
}

func (i *Inline) Variables() []string {
	var out []string
	add := func(variable string) {
		if variable != "" && !slices.Contains(out, variable) {
			out = append(out, variable)
		}
	}
	if p := i.Postgres; p != nil {
		add(p.URL)
		add(p.Host.Variable)
		add(p.Database.Variable)
		add(p.Username.Variable)
		add(p.Password)
		if p.TLS != nil {
			add(p.TLS.CA)
		}
	}
	if b := i.Bucket; b != nil {
		add(b.Endpoint.Variable)
		add(b.Region.Variable)
		add(b.Bucket.Variable)
		add(b.Prefix.Variable)
		add(b.AccessKeyID)
		add(b.SecretAccessKey)
		add(b.PublicBaseURL.Variable)
	}
	slices.Sort(out)
	return out
}

func normalizeBindings(raw configdoc.Bindings) ([]Binding, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]Binding, 0, len(raw))
	for _, key := range slices.Sorted(maps.Keys(raw)) {
		typ, bindable := naming.ResourceTypeNamed(key)
		if _, ok := naming.BindableAs(typ); !bindable || !ok {
			return nil, fmt.Errorf("`bindings` is keyed by %q, and nothing publishes a record of that type — the types that can be bound are %s",
				key, strings.Join(configdoc.BindableTypes(), ", "))
		}
		named := raw[key]
		for _, declared := range slices.Sorted(maps.Keys(named)) {
			if strings.TrimSpace(declared) == "" {
				return nil, fmt.Errorf("`bindings.%s` is keyed by an empty name — the key is the name an app declares the resource under, and the value is the record it binds, written as \"@<name>\"", key)
			}
			bindings, err := normalizeBinding(key, typ, declared, named[declared])
			if err != nil {
				return nil, err
			}
			out = append(out, bindings...)
		}
	}
	slices.SortFunc(out, func(a, b Binding) int {
		if a.Type != b.Type {
			return cmp.Compare(a.Type, b.Type)
		}
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.Tier, b.Tier)
	})
	return out, nil
}

func normalizeBinding(key string, typ resourcesv1.ResourceType, declared string, raw configdoc.Binding) ([]Binding, error) {
	path := "bindings." + key + "." + declared
	if raw.Production == nil && raw.Preview == nil {
		external, err := publishedName(path, raw.Published)
		if err != nil {
			return nil, err
		}
		return []Binding{{Type: typ, Name: declared, External: external}}, nil
	}
	if raw.Production == raw.Preview {
		inline, err := normalizeInline(path, raw.Production)
		if err != nil {
			return nil, err
		}
		return []Binding{{Type: typ, Name: declared, Inline: inline}}, nil
	}
	var out []Binding
	for _, tiered := range []struct {
		tier   environmentv1.Tier
		key    string
		record *configdoc.InlineRecord
	}{
		{environmentv1.Tier_TIER_PRODUCTION, configdoc.TierProduction, raw.Production},
		{environmentv1.Tier_TIER_PREVIEW, configdoc.TierPreview, raw.Preview},
	} {
		if tiered.record == nil {
			continue
		}
		inline, err := normalizeInline(path+"."+tiered.key, tiered.record)
		if err != nil {
			return nil, err
		}
		out = append(out, Binding{Type: typ, Name: declared, Tier: tiered.tier, Inline: inline})
	}
	return out, nil
}

func publishedName(path, spelled string) (string, error) {
	value := strings.TrimSpace(spelled)
	external, marked := strings.CutPrefix(value, "@")
	if strings.TrimSpace(external) == "" {
		return "", fmt.Errorf("`%s` names no published record — a binding always spells out the name the record is published under, even when it matches, as \"@<name>\"", path)
	}
	if !marked {
		return "", fmt.Errorf("`%s` is %q; a published record is written %q — the @ marks the name the record is published under, apart from the name the app declares", path, value, "@"+value)
	}
	if trimmed := strings.TrimLeftFunc(external, unicode.IsSpace); trimmed != external {
		return "", fmt.Errorf("`%s` is %q; the published name starts right after the @, written %q", path, value, "@"+trimmed)
	}
	if strings.Contains(external, naming.KeySeparator) {
		return "", fmt.Errorf("published name %q may not contain %q: it separates the fields of the key the record is stored under", external, naming.KeySeparator)
	}
	if naming.IsInlineRecord(external) {
		return "", fmt.Errorf("`%s` binds %q, and a name starting %q is the record ocel keeps for a binding written inline — write that binding's record itself, or publish yours under another name", path, value, naming.InlineRecordPrefix)
	}
	return external, nil
}

func normalizeInline(path string, record *configdoc.InlineRecord) (*Inline, error) {
	if record.Postgres != nil {
		postgres, err := normalizePostgres(path, record.Postgres)
		if err != nil {
			return nil, err
		}
		return &Inline{Postgres: postgres}, nil
	}
	if record.Bucket != nil {
		bucket, err := normalizeBucket(path, record.Bucket)
		if err != nil {
			return nil, err
		}
		return &Inline{Bucket: bucket}, nil
	}
	return nil, fmt.Errorf("`%s` holds no record", path)
}

var variableKey = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

const ocelPrefix = "OCEL_"

func variableOf(path string, ref *configdoc.Ref) (string, error) {
	key := ref.Env
	if !variableKey.MatchString(key) {
		return "", fmt.Errorf("`%s` reads the variable %q, and a variable is named in upper-case letters, digits and underscores, not starting with a digit", path, key)
	}
	if strings.HasPrefix(key, ocelPrefix) {
		return "", fmt.Errorf("`%s` reads the variable %q, and names starting %s are the ones ocel writes itself — name it something else", path, key, ocelPrefix)
	}
	return key, nil
}

func requiredVariable(path, field string, ref *configdoc.Ref) (string, error) {
	if ref == nil {
		return "", fmt.Errorf("`%s` has no %s", path, field)
	}
	return variableOf(path+"."+field, ref)
}

func textOf(path, field string, text *configdoc.Text) (Value, error) {
	at := path + "." + field
	if text == nil {
		return Value{}, fmt.Errorf("`%s` has no %s", path, field)
	}
	if text.Ref != nil {
		variable, err := variableOf(at, text.Ref)
		return Value{Variable: variable}, err
	}
	literal := strings.TrimSpace(text.Literal)
	if literal == "" {
		return Value{}, fmt.Errorf("`%s` is empty — give it a value, or an ocel variable written { \"$env\": \"NAME\" }", at)
	}
	return Value{Literal: literal}, nil
}

func normalizePostgres(path string, raw *configdoc.PostgresBinding) (*PostgresInline, error) {
	if raw.URL != nil {
		url, err := variableOf(path+".url", raw.URL)
		if err != nil {
			return nil, err
		}
		return &PostgresInline{URL: url}, nil
	}
	if raw.Host == nil && raw.Database == nil && raw.Username == nil && raw.Password == nil {
		return nil, fmt.Errorf("`%s` holds no postgres record — give it url, or host, database, username and password", path)
	}
	out := &PostgresInline{Port: raw.Port}
	var err error
	if out.Host, err = textOf(path, "host", raw.Host); err != nil {
		return nil, err
	}
	if out.Database, err = textOf(path, "database", raw.Database); err != nil {
		return nil, err
	}
	if out.Username, err = textOf(path, "username", raw.Username); err != nil {
		return nil, err
	}
	if out.Password, err = requiredVariable(path, "password", raw.Password); err != nil {
		return nil, err
	}
	if raw.Port != 0 && (raw.Port < 1 || raw.Port > 65535) {
		return nil, fmt.Errorf("`%s.port` is %d, and a port is 1 to 65535", path, raw.Port)
	}
	if raw.TLS != nil {
		out.TLS = &PostgresTLS{Mode: raw.TLS.Mode}
		if raw.TLS.CA != nil {
			if out.TLS.CA, err = variableOf(path+".tls.ca", raw.TLS.CA); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func optionalText(path, field string, text *configdoc.Text) (Value, error) {
	if text == nil {
		return Value{}, nil
	}
	return textOf(path, field, text)
}

func normalizeBucket(path string, raw *configdoc.BucketBinding) (*BucketInline, error) {
	out := &BucketInline{PathStyle: raw.PathStyle}
	var err error
	if out.Endpoint, err = textOf(path, "endpoint", raw.Endpoint); err != nil {
		return nil, err
	}
	if literal := out.Endpoint.Literal; literal != "" && !strings.HasPrefix(literal, "https://") && !strings.HasPrefix(literal, "http://") {
		return nil, fmt.Errorf("`%s.endpoint` is %q, and an endpoint is the store's address with its scheme, such as \"https://%s\"", path, literal, literal)
	}
	if out.Region, err = textOf(path, "region", raw.Region); err != nil {
		return nil, err
	}
	if out.Bucket, err = textOf(path, "bucket", raw.Bucket); err != nil {
		return nil, err
	}
	if out.Prefix, err = optionalText(path, "prefix", raw.Prefix); err != nil {
		return nil, err
	}
	if out.PublicBaseURL, err = optionalText(path, "publicBaseUrl", raw.PublicBaseURL); err != nil {
		return nil, err
	}
	if out.AccessKeyID, err = requiredVariable(path, "accessKeyId", raw.AccessKeyID); err != nil {
		return nil, err
	}
	if out.SecretAccessKey, err = requiredVariable(path, "secretAccessKey", raw.SecretAccessKey); err != nil {
		return nil, err
	}
	return out, nil
}
