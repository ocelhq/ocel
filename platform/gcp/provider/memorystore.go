package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/api/googleapi"
)

const memorystoreHost = "https://memorystore.googleapis.com"

const memorystoreTimeout = 60 * time.Second

const (
	instanceActive   = "ACTIVE"
	instanceCreating = "CREATING"
	instanceUpdating = "UPDATING"
	tokenActive      = "ACTIVE"
	primaryEndpoint  = "CONNECTION_TYPE_PRIMARY"
	defaultTokenUser = "default"
)

type memorystoreInstance struct {
	Name                      string             `json:"name,omitempty"`
	State                     string             `json:"state,omitempty"`
	Labels                    map[string]string  `json:"labels,omitempty"`
	Mode                      string             `json:"mode,omitempty"`
	ShardCount                int                `json:"shardCount,omitempty"`
	ReplicaCount              *int               `json:"replicaCount,omitempty"`
	NodeType                  string             `json:"nodeType,omitempty"`
	EngineVersion             string             `json:"engineVersion,omitempty"`
	EngineConfigs             map[string]string  `json:"engineConfigs,omitempty"`
	AuthorizationMode         string             `json:"authorizationMode,omitempty"`
	TransitEncryptionMode     string             `json:"transitEncryptionMode,omitempty"`
	PersistenceConfig         *persistenceConfig `json:"persistenceConfig,omitempty"`
	DeletionProtectionEnabled *bool              `json:"deletionProtectionEnabled,omitempty"`
	Endpoints                 []instanceEndpoint `json:"endpoints,omitempty"`
}

type persistenceConfig struct {
	Mode      string     `json:"mode,omitempty"`
	AOFConfig *aofConfig `json:"aofConfig,omitempty"`
}

type aofConfig struct {
	AppendFsync string `json:"appendFsync,omitempty"`
}

type instanceEndpoint struct {
	Connections []endpointConnection `json:"connections,omitempty"`
}

type endpointConnection struct {
	PSCAutoConnection *pscAutoConnection `json:"pscAutoConnection,omitempty"`
}

type pscAutoConnection struct {
	ProjectID      string `json:"projectId,omitempty"`
	Network        string `json:"network,omitempty"`
	IPAddress      string `json:"ipAddress,omitempty"`
	Port           int    `json:"port,omitempty"`
	ConnectionType string `json:"connectionType,omitempty"`
}

type memorystoreOperation struct {
	Name     string             `json:"name"`
	Done     bool               `json:"done"`
	Error    *operationStatus   `json:"error,omitempty"`
	Metadata *operationMetadata `json:"metadata,omitempty"`
}

type operationStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type operationMetadata struct {
	CreateTime string `json:"createTime,omitempty"`
	EndTime    string `json:"endTime,omitempty"`
}

type certificateAuthority struct {
	ManagedServerCA *struct {
		CACerts []struct {
			Certificates []string `json:"certificates"`
		} `json:"caCerts"`
	} `json:"managedServerCa,omitempty"`
}

type authToken struct {
	Name       string `json:"name"`
	Token      string `json:"token"`
	State      string `json:"state"`
	CreateTime string `json:"createTime"`
}

type memorystore struct {
	base   string
	tokens TokenSource
	client *http.Client
}

func (p *Provider) memorystore() memorystore {
	base := memorystoreHost
	if p.emulated() {
		base = strings.TrimRight(p.endpoint, "/")
	}
	var tokens TokenSource
	if !p.emulated() {
		tokens = p.tokens
	}
	return memorystore{base: base, tokens: tokens, client: &http.Client{Timeout: memorystoreTimeout}}
}

func (m memorystore) readInstance(ctx context.Context, name string) (*memorystoreInstance, error) {
	var instance memorystoreInstance
	if err := m.call(ctx, http.MethodGet, name, nil, nil, &instance); err != nil {
		return nil, err
	}
	return &instance, nil
}

func (m memorystore) createInstance(ctx context.Context, location, id string, instance *memorystoreInstance) (*memorystoreOperation, error) {
	var started memorystoreOperation
	err := m.call(ctx, http.MethodPost, location+"/instances", url.Values{"instanceId": {id}, "requestId": {uuid.NewString()}}, instance, &started)
	return &started, err
}

func (m memorystore) updateInstance(ctx context.Context, name string, mask []string, instance *memorystoreInstance) (*memorystoreOperation, error) {
	var started memorystoreOperation
	query := url.Values{"updateMask": {strings.Join(mask, ",")}, "requestId": {uuid.NewString()}}
	err := m.call(ctx, http.MethodPatch, name, query, instance, &started)
	return &started, err
}

func (m memorystore) deleteInstance(ctx context.Context, name string) (*memorystoreOperation, error) {
	var started memorystoreOperation
	err := m.call(ctx, http.MethodDelete, name, url.Values{"requestId": {uuid.NewString()}}, nil, &started)
	return &started, err
}

func (m memorystore) readSettledInstance(ctx context.Context, name, store string) (*memorystoreInstance, error) {
	return waiting(ctx, memorystorePatience, "kv "+store+" to finish what Memorystore is doing to it", func() (*memorystoreInstance, error) {
		return m.readInstance(ctx, name)
	}, func(instance *memorystoreInstance) bool { return !isSettling(instance.State) })
}

func isSettling(state string) bool { return state == instanceCreating || state == instanceUpdating }

func (m memorystore) readOperation(ctx context.Context, name string) (*memorystoreOperation, error) {
	var polled memorystoreOperation
	err := m.call(ctx, http.MethodGet, name, nil, nil, &polled)
	return &polled, err
}

func (m memorystore) readCertificateAuthority(ctx context.Context, instance string) (*certificateAuthority, error) {
	var authority certificateAuthority
	err := m.call(ctx, http.MethodGet, instance+"/certificateAuthority", nil, nil, &authority)
	return &authority, err
}

func (m memorystore) listDefaultTokens(ctx context.Context, instance string) ([]authToken, error) {
	var listed struct {
		AuthTokens []authToken `json:"authTokens"`
	}
	err := m.call(ctx, http.MethodGet, instance+"/tokenAuthUsers/"+defaultTokenUser+"/authTokens", nil, nil, &listed)
	return listed.AuthTokens, err
}

func (m memorystore) call(ctx context.Context, method, name string, query url.Values, body, into any) error {
	var sent []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		sent = encoded
	}
	target := m.base + "/v1/" + strings.TrimPrefix(name, "/")
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	answer, _, err := asked(ctx, func() ([]byte, int, error) {
		return m.send(ctx, method, target, sent)
	})
	if err != nil {
		return err
	}
	if into == nil || len(answer) == 0 {
		return nil
	}
	if err := json.Unmarshal(answer, into); err != nil {
		return fmt.Errorf("read what Memorystore answered %s %s with: %w", method, name, err)
	}
	return nil
}

func (m memorystore) send(ctx context.Context, method, target string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.tokens != nil {
		token, err := m.tokens.Token(ctx)
		if err != nil {
			return nil, 0, unauthenticated()
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, resp.StatusCode, memorystoreRefusal(resp.StatusCode, answer)
	}
	return answer, resp.StatusCode, nil
}

func memorystoreRefusal(code int, answer []byte) error {
	var said struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	message := strings.TrimSpace(string(answer))
	if json.Unmarshal(answer, &said) == nil && said.Error.Message != "" {
		message = said.Error.Message
	}
	return &googleapi.Error{Code: code, Message: message}
}

func (m memorystore) awaited(ctx context.Context, doing string, started *memorystoreOperation) (*memorystoreOperation, error) {
	return awaitOperation(ctx, memorystorePatience, "Memorystore", doing, started, (*memorystoreOperation).outcome,
		func(name string) (*memorystoreOperation, error) { return m.readOperation(ctx, name) })
}

func (o *memorystoreOperation) outcome() operationOutcome {
	outcome := operationOutcome{name: o.Name, done: o.Done}
	if o.Error != nil {
		outcome.failure = failureOf(int64(o.Error.Code), o.Error.Message)
	}
	return outcome
}

var memorystorePatience = patience{attempts: 180, ceiling: 15 * time.Second}
