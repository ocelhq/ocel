package envsource

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/variablestore"
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
	variableNameText = regexp.MustCompile(`^[^#\x00-\x1f\x7f]*$`)
)

func refuseMalformedInfisical(options InfisicalOptions) error {
	if err := refuseBlank("project", options.Project, "the id of the Infisical project this tier reads"); err != nil {
		return err
	}
	if err := refuseBlank("environment", options.Environment, "the slug of the Infisical environment this tier reads"); err != nil {
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

func decodeDevInfisical(options InfisicalOptions) (behaviour, error) {
	if err := refuseMalformedInfisical(options); err != nil {
		return behaviour{}, err
	}
	switch {
	case options.Auth != nil:
		return behaviour{}, &OptionError{Field: "auth", Reason: "is for production and preview: ocel dev reads Infisical as you, from INFISICAL_TOKEN or else the infisical CLI you are logged in to — drop it"}
	case options.Write != "":
		return behaviour{}, &OptionError{Field: "write", Reason: "is for production and preview: ocel dev only reads Infisical — drop it"}
	}
	options = options.Normalize()
	return behaviour{
		id: options.ID(),
		open: func(dir string, lookupEnv func(string) (string, bool)) (Source, error) {
			return openInfisicalAsDeveloper(options, dir, lookupEnv)
		},
	}, nil
}

func decodeDeployedInfisical(options InfisicalOptions) (behaviour, error) {
	if err := refuseMalformedInfisical(options); err != nil {
		return behaviour{}, err
	}
	if options.Auth == nil {
		return behaviour{}, &OptionError{Field: "auth", Reason: "is required: production and preview read Infisical as a machine identity, keyed by one of universal, identity"}
	}
	if options.Write != "" {
		if err := refuseNotOneOf("write", options.Write, WriteNever, WriteMissing, WriteValues); err != nil {
			return behaviour{}, err
		}
	}
	if err := refuseMalformedAuth(options.Auth); err != nil {
		return behaviour{}, err
	}
	options = options.Normalize()
	location := []string{options.Host, options.Project, options.Environment, options.Path, options.Auth.method()}
	if options.Auth.Identity != nil {
		location = append(location, options.Auth.Identity.IdentityID)
	}
	return behaviour{
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

func refuseMalformedAuth(auth *InfisicalAuth) error {
	if (auth.Universal == nil) == (auth.Identity == nil) {
		return &OptionError{Field: "auth", Reason: "must set exactly one of the keys universal, identity"}
	}
	if auth.Identity != nil {
		return refuseBlank("auth.identity.identityId", auth.Identity.IdentityID, "the id of the Infisical machine identity to sign in as")
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

const infisicalHiddenValue = "<hidden-by-infisical>"

type infisical struct {
	options InfisicalOptions
	client  *infisicalClient

	mu    sync.Mutex
	orgID string
}

func NewInfisical(options InfisicalOptions, credential Credential, client *http.Client) Source {
	if client == nil {
		client = &http.Client{Timeout: infisicalRequestTimeout}
	}
	return &infisical{options: options, client: &infisicalClient{host: options.Host, http: client, credential: credential}}
}

func (s *infisical) ID() string { return s.options.ID() }

type infisicalSecret struct {
	ID          string `json:"id"`
	Key         string `json:"secretKey"`
	Value       string `json:"secretValue"`
	Version     int64  `json:"version"`
	ValueHidden bool   `json:"secretValueHidden"`
}

func (s *infisical) Read(ctx context.Context, folders []string) (map[variablestore.Cell]Value, error) {
	s.cacheOrgID(ctx)
	out := map[variablestore.Cell]Value{}
	for _, folder := range folders {
		at, err := s.options.secretPath(folder)
		if err != nil {
			return nil, err
		}
		query := url.Values{
			"projectId":              {s.options.Project},
			"environment":            {s.options.Environment},
			"secretPath":             {at},
			"recursive":              {"false"},
			"expandSecretReferences": {"true"},
			"includeImports":         {"true"},
			"viewSecretValue":        {"true"},
		}
		var listed struct {
			Secrets []infisicalSecret `json:"secrets"`
			Imports []struct {
				Secrets []infisicalSecret `json:"secrets"`
			} `json:"imports"`
		}
		err = s.client.call(ctx, http.MethodGet, "/api/v4/secrets?"+query.Encode(), nil, &listed, true)
		if folder != "" && isInfisicalStatus(err, http.StatusNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s in Infisical's %s environment: %w", at, s.options.Environment, err)
		}
		found := map[string]infisicalSecret{}
		for _, imported := range listed.Imports {
			for _, secret := range imported.Secrets {
				if _, seen := found[secret.Key]; !seen {
					found[secret.Key] = secret
				}
			}
		}
		for _, secret := range listed.Secrets {
			found[secret.Key] = secret
		}
		for key, secret := range found {
			if secret.ValueHidden || secret.Value == infisicalHiddenValue {
				return nil, fmt.Errorf("%s in %s is listed by Infisical with its value hidden from this identity: grant it read access to secret values", key, at)
			}
			if secret.Value == "" {
				continue
			}
			out[variablestore.Cell{Folder: folder, Key: key}] = Value{Plaintext: []byte(secret.Value), Version: infisicalVersion(secret)}
		}
	}
	return out, nil
}

func (s *infisical) cacheOrgID(ctx context.Context) {
	s.mu.Lock()
	known := s.orgID != ""
	s.mu.Unlock()
	if known {
		return
	}
	var described struct {
		Project struct {
			OrgID string `json:"orgId"`
		} `json:"project"`
	}
	if err := s.client.call(ctx, http.MethodGet, "/api/v1/projects/"+url.PathEscape(s.options.Project), nil, &described, true); err != nil {
		return
	}
	s.mu.Lock()
	s.orgID = described.Project.OrgID
	s.mu.Unlock()
}

func (s *infisical) Create(ctx context.Context, at variablestore.Cell, value []byte, description string) error {
	if s.options.Write != WriteMissing && s.options.Write != WriteValues {
		return ErrReadOnly
	}
	secretPath, err := s.options.secretPath(at.Folder)
	if err != nil {
		return err
	}
	body := map[string]string{
		"projectId":     s.options.Project,
		"environment":   s.options.Environment,
		"secretPath":    secretPath,
		"secretValue":   string(value),
		"secretComment": description,
		"type":          "shared",
	}
	err = s.createSecret(ctx, at.Key, body)
	if isInfisicalStatus(err, http.StatusNotFound) {
		if err := s.ensureFolder(ctx, secretPath); err != nil {
			return err
		}
		err = s.createSecret(ctx, at.Key, body)
	}
	return err
}

func (s *infisical) createSecret(ctx context.Context, key string, body map[string]string) error {
	var created struct {
		Secret   json.RawMessage `json:"secret"`
		Approval json.RawMessage `json:"approval"`
	}
	err := s.client.call(ctx, http.MethodPost, "/api/v4/secrets/"+url.PathEscape(key), body, &created, false)
	if isInfisicalStatus(err, http.StatusBadRequest) && isInfisicalExists(err) {
		return fmt.Errorf("%s in %s: %w", key, body["secretPath"], ErrExists)
	}
	if err != nil {
		return fmt.Errorf("create %s in %s: %w", key, body["secretPath"], err)
	}
	if len(created.Secret) == 0 && len(created.Approval) > 0 {
		return fmt.Errorf("%s in %s: %w", key, body["secretPath"], ErrAwaitingApproval)
	}
	return nil
}

func (s *infisical) Update(ctx context.Context, at variablestore.Cell, value []byte, copiedVersion string) error {
	if s.options.Write != WriteValues {
		return ErrReadOnly
	}
	secretPath, err := s.options.secretPath(at.Folder)
	if err != nil {
		return err
	}
	neverCopied := copiedVersion == ""
	query := url.Values{
		"projectId":              {s.options.Project},
		"environment":            {s.options.Environment},
		"secretPath":             {secretPath},
		"type":                   {"shared"},
		"viewSecretValue":        {strconv.FormatBool(neverCopied)},
		"expandSecretReferences": {"false"},
		"includeImports":         {"false"},
	}
	var current struct {
		Secret infisicalSecret `json:"secret"`
	}
	err = s.client.call(ctx, http.MethodGet, "/api/v4/secrets/"+url.PathEscape(at.Key)+"?"+query.Encode(), nil, &current, true)
	if isInfisicalStatus(err, http.StatusNotFound) {
		return fmt.Errorf("%s in %s: %w", at.Key, secretPath, ErrNotInFolder)
	}
	if err != nil {
		return fmt.Errorf("read %s in %s: %w", at.Key, secretPath, err)
	}
	unchanged := infisicalVersion(current.Secret) == sourceVersionOf(copiedVersion)
	if neverCopied {
		unchanged = current.Secret.Value == "" && !current.Secret.ValueHidden
	}
	if !unchanged {
		return fmt.Errorf("%s in %s: %w", at.Key, secretPath, ErrChangedSinceRead)
	}
	var updated struct {
		Secret   json.RawMessage `json:"secret"`
		Approval json.RawMessage `json:"approval"`
	}
	err = s.client.call(ctx, http.MethodPatch, "/api/v4/secrets/"+url.PathEscape(at.Key), map[string]string{
		"projectId":   s.options.Project,
		"environment": s.options.Environment,
		"secretPath":  secretPath,
		"secretValue": string(value),
		"type":        "shared",
	}, &updated, false)
	if err != nil {
		return fmt.Errorf("update %s in %s: %w", at.Key, secretPath, err)
	}
	if len(updated.Secret) == 0 && len(updated.Approval) > 0 {
		return fmt.Errorf("%s in %s: %w", at.Key, secretPath, ErrAwaitingApproval)
	}
	return nil
}

func infisicalVersion(secret infisicalSecret) string {
	return fmt.Sprintf("%s@%d", secret.ID, secret.Version)
}

func (s *infisical) ensureFolder(ctx context.Context, secretPath string) error {
	parent := "/"
	for name := range strings.SplitSeq(strings.Trim(secretPath, "/"), "/") {
		if name == "" {
			continue
		}
		err := s.client.call(ctx, http.MethodPost, "/api/v2/folders", map[string]string{
			"projectId":   s.options.Project,
			"environment": s.options.Environment,
			"name":        name,
			"path":        parent,
		}, nil, false)
		if err != nil && !isInfisicalExists(err) {
			return fmt.Errorf("create folder %s in Infisical: %w", path.Join(parent, name), err)
		}
		parent = path.Join(parent, name)
	}
	return nil
}

func (s *infisical) URL(at variablestore.Cell) string {
	s.mu.Lock()
	org := s.orgID
	s.mu.Unlock()
	secretPath, err := s.options.secretPath(at.Folder)
	if org == "" || err != nil {
		return ""
	}
	query := url.Values{"secretPath": {secretPath}}
	if at.Key != "" {
		query.Set("search", at.Key)
	}
	return s.options.Host + "/organizations/" + url.PathEscape(org) + "/projects/secret-management/" +
		url.PathEscape(s.options.Project) + "/secrets/" + url.PathEscape(s.options.Environment) + "?" + query.Encode()
}
