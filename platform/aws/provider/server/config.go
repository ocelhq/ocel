package server

import (
	"context"
	"errors"
	"sync"

	connect "connectrpc.com/connect"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

type providerConfig struct {
	Region       string
	Transforms   []string
	Certificates map[string]string
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

var errForeignProvider = errors.New("this provider deploys into AWS, and the session was configured for another")

func (s *Server) Configure(_ context.Context, req *deploymentsv1.ConfigureRequest) (*deploymentsv1.ConfigureResponse, error) {
	switch provider := req.GetConfig().GetProvider().(type) {
	case nil:
		s.config.set(providerConfig{})
	case *deploymentsv1.ProviderConfig_Aws:
		s.config.set(providerConfig{
			Region:       provider.Aws.GetRegion(),
			Transforms:   provider.Aws.GetTransforms(),
			Certificates: provider.Aws.GetCertificates(),
		})
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errForeignProvider)
	}
	return &deploymentsv1.ConfigureResponse{}, nil
}
