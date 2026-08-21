package server

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"

	connect "connectrpc.com/connect"

	"google.golang.org/protobuf/encoding/protojson"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type providerConfig struct {
	Region       string            `json:"region"`
	Transforms   []string          `json:"transforms"`
	Certificates map[string]string `json:"certificates"`
}

type sessionConfig struct {
	mu    sync.RWMutex
	value providerConfig
}

func (c *sessionConfig) get() providerConfig {
	if c == nil {
		return providerConfig{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.value
}

func (c *sessionConfig) set(value providerConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value = value
}

// TODO(#514): lift Options into providerkit unchanged once the kit exists.
func Options[T any](config *contractv1.ProviderConfig) (T, error) {
	var options T
	fields := config.GetOptions()
	if len(fields.GetFields()) == 0 {
		return options, nil
	}
	raw, err := protojson.Marshal(fields)
	if err != nil {
		return options, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&options); err != nil {
		var zero T
		return zero, err
	}
	return options, nil
}

func (s *Server) Configure(_ context.Context, req *contractv1.ConfigureRequest) (*contractv1.ConfigureResponse, error) {
	options, err := Options[providerConfig](req.GetConfig())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	s.config.set(options)
	return &contractv1.ConfigureResponse{}, nil
}
