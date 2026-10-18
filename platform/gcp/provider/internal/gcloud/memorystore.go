package gcloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/api/googleapi"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const memorystoreHost = "https://memorystore.googleapis.com"

const memorystoreTimeout = 60 * time.Second

const (
	InstanceActive   = "ACTIVE"
	instanceCreating = "CREATING"
	instanceUpdating = "UPDATING"
	TokenActive      = "ACTIVE"
	PrimaryEndpoint  = "CONNECTION_TYPE_PRIMARY"
	DefaultTokenUser = "default"
)

type MemorystoreInstance struct {
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
	PersistenceConfig         *PersistenceConfig `json:"persistenceConfig,omitempty"`
	DeletionProtectionEnabled *bool              `json:"deletionProtectionEnabled,omitempty"`
	Endpoints                 []InstanceEndpoint `json:"endpoints,omitempty"`
}

type PersistenceConfig struct {
	Mode      string     `json:"mode,omitempty"`
	AOFConfig *AOFConfig `json:"aofConfig,omitempty"`
}

type AOFConfig struct {
	AppendFsync string `json:"appendFsync,omitempty"`
}

type InstanceEndpoint struct {
	Connections []EndpointConnection `json:"connections,omitempty"`
}

type EndpointConnection struct {
	PSCAutoConnection *PSCAutoConnection `json:"pscAutoConnection,omitempty"`
}

type PSCAutoConnection struct {
	ProjectID      string `json:"projectId,omitempty"`
	Network        string `json:"network,omitempty"`
	IPAddress      string `json:"ipAddress,omitempty"`
	Port           int    `json:"port,omitempty"`
	ConnectionType string `json:"connectionType,omitempty"`
}

type MemorystoreOperation struct {
	Name     string             `json:"name"`
	Done     bool               `json:"done"`
	Error    *OperationStatus   `json:"error,omitempty"`
	Metadata *OperationMetadata `json:"metadata,omitempty"`
}

type OperationStatus struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type OperationMetadata struct {
	CreateTime string `json:"createTime,omitempty"`
	EndTime    string `json:"endTime,omitempty"`
}

type CertificateAuthority struct {
	ManagedServerCA *struct {
		CACerts []struct {
			Certificates []string `json:"certificates"`
		} `json:"caCerts"`
	} `json:"managedServerCa,omitempty"`
}

type AuthToken struct {
	Name       string `json:"name"`
	Token      string `json:"token"`
	State      string `json:"state"`
	CreateTime string `json:"createTime"`
}

type Memorystore struct {
	Base   string
	tokens TokenSource
	Client *http.Client
}

func NewMemorystore(endpoint string, tokens TokenSource) Memorystore {
	base := memorystoreHost
	if endpoint != "" {
		base = strings.TrimRight(endpoint, "/")
		tokens = nil
	}
	return Memorystore{Base: base, tokens: tokens, Client: &http.Client{Timeout: memorystoreTimeout}}
}

func (m Memorystore) ReadInstance(ctx context.Context, name string) (*MemorystoreInstance, error) {
	var instance MemorystoreInstance
	if err := m.call(ctx, http.MethodGet, name, nil, nil, &instance); err != nil {
		return nil, err
	}
	return &instance, nil
}

func (m Memorystore) CreateInstance(ctx context.Context, location, id string, instance *MemorystoreInstance) (*MemorystoreOperation, error) {
	var started MemorystoreOperation
	err := m.call(ctx, http.MethodPost, location+"/instances", url.Values{"instanceId": {id}, "requestId": {uuid.NewString()}}, instance, &started)
	return &started, err
}

func (m Memorystore) UpdateInstance(ctx context.Context, name string, mask []string, instance *MemorystoreInstance) (*MemorystoreOperation, error) {
	var started MemorystoreOperation
	query := url.Values{"updateMask": {strings.Join(mask, ",")}, "requestId": {uuid.NewString()}}
	err := m.call(ctx, http.MethodPatch, name, query, instance, &started)
	return &started, err
}

func (m Memorystore) DeleteInstance(ctx context.Context, name string) (*MemorystoreOperation, error) {
	var started MemorystoreOperation
	err := m.call(ctx, http.MethodDelete, name, url.Values{"requestId": {uuid.NewString()}}, nil, &started)
	return &started, err
}

func (m Memorystore) ReadSettledInstance(ctx context.Context, name, store string) (*MemorystoreInstance, error) {
	return Waiting(ctx, memorystorePatience, "kv "+store+" to finish what Memorystore is doing to it", func() (*MemorystoreInstance, error) {
		return m.ReadInstance(ctx, name)
	}, func(instance *MemorystoreInstance) bool { return !IsSettling(instance.State) })
}

func IsSettling(state string) bool { return state == instanceCreating || state == instanceUpdating }

func (m Memorystore) ListInstances(ctx context.Context, location, filter string, pageSize int, pageToken string) ([]MemorystoreInstance, string, error) {
	var listed struct {
		Instances     []MemorystoreInstance `json:"instances"`
		NextPageToken string                `json:"nextPageToken"`
	}
	query := url.Values{"filter": {filter}, "pageSize": {strconv.Itoa(pageSize)}}
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}
	err := m.call(ctx, http.MethodGet, location+"/instances", query, nil, &listed)
	return listed.Instances, listed.NextPageToken, err
}

func (m Memorystore) readOperation(ctx context.Context, name string) (*MemorystoreOperation, error) {
	var polled MemorystoreOperation
	err := m.call(ctx, http.MethodGet, name, nil, nil, &polled)
	return &polled, err
}

func (m Memorystore) ReadCertificateAuthority(ctx context.Context, instance string) (*CertificateAuthority, error) {
	var authority CertificateAuthority
	err := m.call(ctx, http.MethodGet, instance+"/certificateAuthority", nil, nil, &authority)
	return &authority, err
}

func (m Memorystore) ListDefaultTokens(ctx context.Context, instance string) ([]AuthToken, error) {
	var listed struct {
		AuthTokens []AuthToken `json:"authTokens"`
	}
	err := m.call(ctx, http.MethodGet, instance+"/tokenAuthUsers/"+DefaultTokenUser+"/authTokens", nil, nil, &listed)
	return listed.AuthTokens, err
}

func (m Memorystore) call(ctx context.Context, method, name string, query url.Values, body, into any) error {
	var sent []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		sent = encoded
	}
	target := m.Base + "/v1/" + strings.TrimPrefix(name, "/")
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	answer, _, err := googleretry.Asked(ctx, func() ([]byte, int, error) {
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

func (m Memorystore) send(ctx context.Context, method, target string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.tokens != nil {
		token, err := m.tokens.Token(ctx)
		if err != nil {
			return nil, 0, Unauthenticated()
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := m.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return nil, resp.StatusCode, newMemorystoreError(resp.StatusCode, answer)
	}
	return answer, resp.StatusCode, nil
}

func newMemorystoreError(code int, answer []byte) error {
	var answered struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	message := strings.TrimSpace(string(answer))
	if json.Unmarshal(answer, &answered) == nil && answered.Error.Message != "" {
		message = answered.Error.Message
	}
	return &googleapi.Error{Code: code, Message: message}
}

func (m Memorystore) Awaited(ctx context.Context, doing string, started *MemorystoreOperation) (*MemorystoreOperation, error) {
	return AwaitOperation(ctx, memorystorePatience, "Memorystore", doing, started, (*MemorystoreOperation).outcome,
		func(name string) (*MemorystoreOperation, error) { return m.readOperation(ctx, name) })
}

func (o *MemorystoreOperation) outcome() OperationOutcome {
	outcome := OperationOutcome{Name: o.Name, Done: o.Done}
	if o.Error != nil {
		outcome.Failure = FailureOf(int64(o.Error.Code), o.Error.Message)
	}
	return outcome
}

var memorystorePatience = Patience{Attempts: 180, Ceiling: 15 * time.Second}
