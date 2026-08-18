package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/workers"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func unauthorized(res *http.Response) bool {
	return res != nil && res.StatusCode == http.StatusUnauthorized
}

func (p *provider) destroyInstance(ctx context.Context, state edge.StackState) error {
	if state[edge.StackKeySecret] == "" {
		return nil
	}
	res, err := p.storeRequest(ctx, state, http.MethodPost, "/destroy", nil, nil)
	if unauthorized(res) {
		return nil
	}
	return err
}

func (p *provider) deleteScript(ctx context.Context, accountID, scriptName string) error {
	_, err := p.client.Workers.Scripts.Delete(ctx, scriptName, workers.ScriptDeleteParams{
		AccountID: cf.F(accountID),
		Force:     cf.F(true),
	})
	if hasStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

func (s *stack) PutStaged(ctx context.Context, record edge.DeploymentRecord) error {
	_, err := s.p.storeRequest(ctx, s.state, http.MethodPut, "/staged", record, nil)
	return err
}

type promoteBody struct {
	edge.Promotion
	Pointer string `json:"pointer,omitempty"`
}

func (s *stack) Promote(ctx context.Context, promotion edge.Promotion, pointer string) error {
	_, err := s.p.storeRequest(ctx, s.state, http.MethodPost, "/promote", promoteBody{Promotion: promotion, Pointer: pointer}, nil)
	return err
}

func (s *stack) History(ctx context.Context, pointer string) ([]edge.HistoryEntry, error) {
	subpath := "/history"
	if pointer != "" {
		subpath += "?pointer=" + url.QueryEscape(pointer)
	}
	var history []edge.HistoryEntry
	if _, err := s.p.storeRequest(ctx, s.state, http.MethodGet, subpath, nil, &history); err != nil {
		return nil, err
	}
	return history, nil
}

func (s *stack) RemovePointer(ctx context.Context, pointer string) (edge.PruneResult, error) {
	var result edge.PruneResult
	if _, err := s.p.storeRequest(ctx, s.state, http.MethodPost, "/remove-pointer", map[string]string{"pointer": pointer}, &result); err != nil {
		return edge.PruneResult{}, err
	}
	return result, nil
}

func (s *stack) Prune(ctx context.Context, keepN int, pointer string) (edge.PruneResult, error) {
	body := map[string]any{"keepN": keepN}
	if pointer != "" {
		body["pointer"] = pointer
	}
	var result edge.PruneResult
	if _, err := s.p.storeRequest(ctx, s.state, http.MethodPost, "/prune", body, &result); err != nil {
		return edge.PruneResult{}, err
	}
	return result, nil
}

func (p *provider) initializeInstance(ctx context.Context, endpoint, slug, bootstrapCred string, present storeIdentity) (storeIdentity, error) {
	body := map[string]any{"ownerToken": present.ownerToken, "secret": present.secret, "force": false}
	var out struct {
		OwnerToken string `json:"ownerToken"`
		Secret     string `json:"secret"`
	}
	if _, err := p.storeRequestTo(ctx, endpoint, slug, bootstrapCred, http.MethodPost, "/initialize", body, &out); err != nil {
		return storeIdentity{}, err
	}
	if out.Secret == "" || out.OwnerToken == "" {
		return storeIdentity{}, fmt.Errorf("deployments store reported no identity for %q", slug)
	}
	return storeIdentity{secret: out.Secret, ownerToken: out.OwnerToken}, nil
}

func (s *stack) SchemaVersion(ctx context.Context) (int, error) {
	var out struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	res, err := s.p.storeRequestTo(ctx, s.state[edge.StackKeyEndpoint], s.state[edge.StackKeySlug], "", http.MethodGet, "/schema-version", nil, &out)
	if err != nil {
		if unauthorized(res) {
			return 0, edge.ErrStoreSchemaUnreadable
		}
		return 0, err
	}
	return out.SchemaVersion, nil
}

func (p *provider) getVersionStamp(ctx context.Context, endpoint, slug, secret string) (string, *http.Response, error) {
	var out struct {
		Version *string `json:"version"`
	}
	res, err := p.storeRequestTo(ctx, endpoint, slug, secret, http.MethodGet, "/version-stamp", nil, &out)
	if err != nil {
		return "", res, err
	}
	if out.Version == nil {
		return "", res, nil
	}
	return *out.Version, res, nil
}

func (p *provider) putVersionStamp(ctx context.Context, endpoint, slug, secret, version string) error {
	_, err := p.storeRequestTo(ctx, endpoint, slug, secret, http.MethodPut, "/version-stamp", map[string]string{"version": version}, nil)
	return err
}

func (p *provider) storeRequest(ctx context.Context, state edge.StackState, method, subpath string, body, out any) (*http.Response, error) {
	return p.storeRequestTo(ctx, state[edge.StackKeyEndpoint], state[edge.StackKeySlug], state[edge.StackKeySecret], method, subpath, body, out)
}

func (p *provider) storeRequestTo(ctx context.Context, endpoint, slug, secret, method, subpath string, body, out any) (*http.Response, error) {
	if endpoint == "" {
		return nil, fmt.Errorf("deployments store: no endpoint; bootstrap the edge first")
	}
	if slug == "" {
		return nil, fmt.Errorf("deployments store: no project slug")
	}

	var encoded []byte
	if body != nil {
		marshalled, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal deployments-store request body: %w", err)
		}
		encoded = marshalled
	}

	var res *http.Response
	var err error
	for attempt := 0; ; attempt++ {
		res, err = p.storeAttempt(ctx, endpoint, slug, secret, method, subpath, encoded, out)
		if attempt == storeMaxAttempts-1 || !storeRetryable(res, err) {
			return res, err
		}
		if waitErr := waitBeforeRetry(ctx, storeRetryDelay(res, attempt, retryJitter())); waitErr != nil {
			return res, err
		}
	}
}

func (p *provider) storeAttempt(ctx context.Context, endpoint, slug, secret, method, subpath string, encoded []byte, out any) (*http.Response, error) {
	var reader io.Reader
	if encoded != nil {
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint+"/"+slug+subpath, reader)
	if err != nil {
		return nil, fmt.Errorf("build deployments-store request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	if encoded != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call deployments store %s %s: %w", method, subpath, err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		respBody, _ := io.ReadAll(res.Body)
		return res, fmt.Errorf("deployments store %s %s: status %d: %s", method, subpath, res.StatusCode, string(respBody))
	}
	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			return res, fmt.Errorf("decode deployments store %s %s response: %w", method, subpath, err)
		}
	}
	return res, nil
}
