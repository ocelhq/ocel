package envsource

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/envvars"
)

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
	options = options.Normalize()
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

func (s *infisical) Read(ctx context.Context, folders []string) (map[envvars.Cell]Value, error) {
	s.cacheOrgID(ctx)
	out := map[envvars.Cell]Value{}
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
			out[envvars.Cell{Folder: folder, Key: key}] = Value{Plaintext: []byte(secret.Value), Version: infisicalVersion(secret)}
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

func (s *infisical) Create(ctx context.Context, at envvars.Cell, value []byte, description string) error {
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

func (s *infisical) Update(ctx context.Context, at envvars.Cell, value []byte, copiedVersion string) error {
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
		return fmt.Errorf("%s is no longer in %s as ocel copied it: %w", at.Key, secretPath, ErrChangedSinceRead)
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

func (s *infisical) URL(at envvars.Cell) string {
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
