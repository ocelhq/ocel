package envsource

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/values"
)

const (
	infisicalAttempts   = 5
	infisicalBackoff    = 250 * time.Millisecond
	infisicalBackoffCap = 8 * time.Second
	infisicalWaitCap    = 60 * time.Second
	infisicalTokenSlack = 60 * time.Second
	infisicalBodyLimit  = 8 << 20
	infisicalHidden     = "<hidden-by-infisical>"
)

type SignedRequest struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
}

type CallerIdentitySigner interface {
	SignCallerIdentity(ctx context.Context) (SignedRequest, error)
}

type IDTokenIssuer interface {
	IDToken(ctx context.Context, audience string) (string, error)
}

type Credential interface {
	login(ctx context.Context, api *infisicalAPI) (session, error)
}

type session struct {
	token   string
	expires time.Time
	static  bool
}

type universalAuth struct{ clientID, clientSecret string }

func UniversalAuth(clientID, clientSecret string) Credential {
	return universalAuth{clientID: clientID, clientSecret: clientSecret}
}

func (u universalAuth) login(ctx context.Context, api *infisicalAPI) (session, error) {
	return api.exchange(ctx, "universal-auth", map[string]string{"clientId": u.clientID, "clientSecret": u.clientSecret})
}

type awsAuth struct {
	identityID string
	signer     CallerIdentitySigner
}

func AWSAuth(identityID string, signer CallerIdentitySigner) Credential {
	return awsAuth{identityID: identityID, signer: signer}
}

func (a awsAuth) login(ctx context.Context, api *infisicalAPI) (session, error) {
	if a.signer == nil {
		return session{}, errors.New("aws auth signs in to Infisical as the target's own AWS role, and this target has none")
	}
	signed, err := a.signer.SignCallerIdentity(ctx)
	if err != nil {
		return session{}, fmt.Errorf("sign the sts:GetCallerIdentity request Infisical checks this role by: %w", err)
	}
	endpoint, err := url.Parse(signed.URL)
	if err != nil {
		return session{}, fmt.Errorf("the signed sts request names no endpoint: %w", err)
	}
	headers := map[string]string{}
	for name, held := range signed.Header {
		if len(held) > 0 && !strings.EqualFold(name, "Content-Length") {
			headers[http.CanonicalHeaderKey(name)] = held[0]
		}
	}
	headers["Host"] = endpoint.Host
	headers["Content-Type"] = "application/x-www-form-urlencoded; charset=utf-8"
	headers["Content-Length"] = strconv.Itoa(len(signed.Body))
	encoded, err := json.Marshal(headers)
	if err != nil {
		return session{}, err
	}
	return api.exchange(ctx, "aws-auth", map[string]string{
		"identityId":           a.identityID,
		"iamHttpRequestMethod": signed.Method,
		"iamRequestBody":       base64.StdEncoding.EncodeToString(signed.Body),
		"iamRequestHeaders":    base64.StdEncoding.EncodeToString(encoded),
	})
}

type gcpAuth struct {
	identityID string
	issuer     IDTokenIssuer
}

func GCPAuth(identityID string, issuer IDTokenIssuer) Credential {
	return gcpAuth{identityID: identityID, issuer: issuer}
}

func (g gcpAuth) login(ctx context.Context, api *infisicalAPI) (session, error) {
	if g.issuer == nil {
		return session{}, errors.New("gcp auth signs in to Infisical as the target's own service account, and this target has none")
	}
	jwt, err := g.issuer.IDToken(ctx, g.identityID)
	if err != nil {
		return session{}, fmt.Errorf("mint the identity token Infisical checks this service account by: %w", err)
	}
	return api.exchange(ctx, "gcp-auth", map[string]string{"identityId": g.identityID, "jwt": jwt})
}

type accessToken string

func AccessToken(token string) Credential { return accessToken(token) }

func (a accessToken) login(context.Context, *infisicalAPI) (session, error) {
	return session{token: string(a), static: true}, nil
}

type infisicalSource struct {
	options InfisicalOptions
	api     *infisicalAPI

	mu    sync.Mutex
	orgID string
}

func NewInfisical(options InfisicalOptions, credential Credential, client *http.Client) Source {
	if client == nil {
		client = http.DefaultClient
	}
	options = options.Normalized()
	return &infisicalSource{options: options, api: &infisicalAPI{host: options.Host, client: client, credential: credential}}
}

func (s *infisicalSource) ID() string {
	return InfisicalID(s.options)
}

func InfisicalID(options InfisicalOptions) string {
	return string(Infisical) + ":" + options.Project + "/" + options.Environment
}

func (s *infisicalSource) Capabilities() Caps {
	return Caps{Read: true, List: true, Write: s.options.Write == WriteMissing, Standing: true}
}

func (s *infisicalSource) secretPath(folder string) string {
	return path.Join(s.options.Path, "/"+strings.TrimPrefix(folder, "/"))
}

type infisicalSecret struct {
	ID          string `json:"id"`
	Key         string `json:"secretKey"`
	Value       string `json:"secretValue"`
	Version     int64  `json:"version"`
	ValueHidden bool   `json:"secretValueHidden"`
}

func (s *infisicalSource) Resolve(ctx context.Context, folders []string) (map[values.Cell]Resolved, error) {
	s.learnOrg(ctx)
	out := map[values.Cell]Resolved{}
	for _, folder := range folders {
		at := s.secretPath(folder)
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
		err := s.api.call(ctx, http.MethodGet, "/api/v4/secrets?"+query.Encode(), nil, &listed, true)
		var refused *infisicalError
		if errors.As(err, &refused) && refused.Status == http.StatusNotFound {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s in Infisical's %s environment: %w", at, s.options.Environment, err)
		}
		held := map[string]infisicalSecret{}
		for _, imported := range listed.Imports {
			for _, secret := range imported.Secrets {
				if _, first := held[secret.Key]; !first {
					held[secret.Key] = secret
				}
			}
		}
		for _, secret := range listed.Secrets {
			held[secret.Key] = secret
		}
		for key, secret := range held {
			if secret.ValueHidden || secret.Value == infisicalHidden {
				return nil, fmt.Errorf("%s in %s is listed by Infisical with its value hidden from this identity: grant it read access to secret values", key, at)
			}
			if secret.Value == "" {
				continue
			}
			out[values.Cell{Folder: folder, Key: key}] = Resolved{Value: []byte(secret.Value), Version: fmt.Sprintf("%s@%d", secret.ID, secret.Version)}
		}
	}
	return out, nil
}

func (s *infisicalSource) learnOrg(ctx context.Context) {
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
	if err := s.api.call(ctx, http.MethodGet, "/api/v1/projects/"+url.PathEscape(s.options.Project), nil, &described, true); err != nil {
		return
	}
	s.mu.Lock()
	s.orgID = described.Project.OrgID
	s.mu.Unlock()
}

func (s *infisicalSource) Put(ctx context.Context, at values.Cell, value []byte, description string) error {
	if s.options.Write != WriteMissing {
		return ErrReadOnly
	}
	secretPath := s.secretPath(at.Folder)
	body := map[string]string{
		"projectId":     s.options.Project,
		"environment":   s.options.Environment,
		"secretPath":    secretPath,
		"secretValue":   string(value),
		"secretComment": description,
		"type":          "shared",
	}
	err := s.create(ctx, at.Key, body)
	var refused *infisicalError
	if errors.As(err, &refused) && refused.Status == http.StatusNotFound {
		if err := s.ensureFolder(ctx, secretPath); err != nil {
			return err
		}
		err = s.create(ctx, at.Key, body)
	}
	return err
}

func (s *infisicalSource) create(ctx context.Context, key string, body map[string]string) error {
	var created struct {
		Secret   json.RawMessage `json:"secret"`
		Approval json.RawMessage `json:"approval"`
	}
	err := s.api.call(ctx, http.MethodPost, "/api/v4/secrets/"+url.PathEscape(key), body, &created, false)
	var refused *infisicalError
	if errors.As(err, &refused) && refused.Status == http.StatusBadRequest && strings.Contains(refused.Message, "already exists") {
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

func (s *infisicalSource) ensureFolder(ctx context.Context, secretPath string) error {
	parent := "/"
	for _, name := range strings.Split(strings.Trim(secretPath, "/"), "/") {
		if name == "" {
			continue
		}
		err := s.api.call(ctx, http.MethodPost, "/api/v2/folders", map[string]string{
			"projectId":   s.options.Project,
			"environment": s.options.Environment,
			"name":        name,
			"path":        parent,
		}, nil, false)
		var refused *infisicalError
		exists := errors.As(err, &refused) && strings.Contains(refused.Message, "already exists")
		if err != nil && !exists {
			return fmt.Errorf("create folder %s in Infisical: %w", path.Join(parent, name), err)
		}
		parent = path.Join(parent, name)
	}
	return nil
}

func (s *infisicalSource) Link(at values.Cell) string {
	s.mu.Lock()
	org := s.orgID
	s.mu.Unlock()
	if org == "" {
		return ""
	}
	query := url.Values{"secretPath": {s.secretPath(at.Folder)}}
	if at.Key != "" {
		query.Set("search", at.Key)
	}
	return s.options.Host + "/organizations/" + url.PathEscape(org) + "/projects/secret-management/" +
		url.PathEscape(s.options.Project) + "/secrets/" + url.PathEscape(s.options.Environment) + "?" + query.Encode()
}

type infisicalAPI struct {
	host       string
	client     *http.Client
	credential Credential

	mu      sync.Mutex
	session session
}

type infisicalError struct {
	Status  int
	Kind    string
	Message string
}

func (e *infisicalError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Infisical answered %d", e.Status)
	}
	return fmt.Sprintf("Infisical answered %d: %s", e.Status, e.Message)
}

func (a *infisicalAPI) exchange(ctx context.Context, method string, body map[string]string) (session, error) {
	var granted struct {
		AccessToken string `json:"accessToken"`
		ExpiresIn   int64  `json:"expiresIn"`
	}
	if err := a.send(ctx, http.MethodPost, "/api/v1/auth/"+method+"/login", "", body, &granted, true); err != nil {
		return session{}, fmt.Errorf("sign in to Infisical with %s: %w", method, err)
	}
	if granted.AccessToken == "" {
		return session{}, fmt.Errorf("sign in to Infisical with %s: the answer carried no access token", method)
	}
	return session{token: granted.AccessToken, expires: time.Now().Add(time.Duration(granted.ExpiresIn) * time.Second)}, nil
}

func (a *infisicalAPI) token(ctx context.Context, fresh bool) (session, error) {
	a.mu.Lock()
	held := a.session
	a.mu.Unlock()
	if !fresh && held.token != "" && (held.static || time.Now().Add(infisicalTokenSlack).Before(held.expires)) {
		return held, nil
	}
	if a.credential == nil {
		return session{}, errors.New("no Infisical credential is configured")
	}
	signed, err := a.credential.login(ctx, a)
	if err != nil {
		return session{}, err
	}
	a.mu.Lock()
	a.session = signed
	a.mu.Unlock()
	return signed, nil
}

func (a *infisicalAPI) call(ctx context.Context, method, target string, body, out any, idempotent bool) error {
	held, err := a.token(ctx, false)
	if err != nil {
		return err
	}
	err = a.send(ctx, method, target, held.token, body, out, idempotent)
	var refused *infisicalError
	if !held.static && errors.As(err, &refused) && refused.Status == http.StatusUnauthorized {
		if held, err = a.token(ctx, true); err != nil {
			return err
		}
		return a.send(ctx, method, target, held.token, body, out, idempotent)
	}
	return err
}

func (a *infisicalAPI) send(ctx context.Context, method, target, token string, body, out any, idempotent bool) error {
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = encoded
	}
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, a.host+target, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := a.client.Do(req)
		if err != nil {
			if !idempotent || attempt >= infisicalAttempts || ctx.Err() != nil {
				return fmt.Errorf("reach Infisical at %s: %w", a.host, err)
			}
			if err := pause(ctx, backoff(attempt)); err != nil {
				return err
			}
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, infisicalBodyLimit))
		resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read Infisical's answer: %w", readErr)
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if out == nil || len(raw) == 0 {
				return nil
			}
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("read Infisical's answer: %w", err)
			}
			return nil
		}
		refused := refusal(resp.StatusCode, raw)
		retryable := resp.StatusCode == http.StatusTooManyRequests ||
			(idempotent && (resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout))
		if !retryable || attempt >= infisicalAttempts {
			return refused
		}
		wait := backoff(attempt)
		if after, ok := retryAfter(resp.Header.Get("Retry-After")); ok {
			wait = after
		}
		if err := pause(ctx, wait); err != nil {
			return err
		}
	}
}

func refusal(status int, raw []byte) *infisicalError {
	var said struct {
		Error   string          `json:"error"`
		Message json.RawMessage `json:"message"`
	}
	out := &infisicalError{Status: status}
	if json.Unmarshal(raw, &said) != nil {
		return out
	}
	out.Kind = said.Error
	var text string
	if json.Unmarshal(said.Message, &text) == nil {
		out.Message = text
	} else if len(said.Message) > 0 {
		out.Message = string(said.Message)
	}
	return out
}

func retryAfter(header string) (time.Duration, bool) {
	if header == "" {
		return 0, false
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || seconds < 0 {
		return 0, false
	}
	return min(time.Duration(seconds)*time.Second, infisicalWaitCap), true
}

func backoff(attempt int) time.Duration {
	ceiling := min(infisicalBackoff<<(attempt-1), infisicalBackoffCap)
	return ceiling/2 + rand.N(ceiling/2+1)
}

func pause(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
