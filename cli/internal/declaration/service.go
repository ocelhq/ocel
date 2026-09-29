package declaration

import (
	"context"
	"net/http"
	"sync"

	"connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/cli/internal/sdkversion"
	"github.com/ocelhq/ocel/cli/internal/version"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/resources/v1/resourcesv1connect"
)

type VariableDeclarations interface {
	DeclareEnv(context.Context, *resourcesv1.DeclareEnvRequest) (*resourcesv1.DeclareEnvResponse, error)
	ReportEnvProblems(context.Context, *resourcesv1.ReportEnvProblemsRequest) (*resourcesv1.ReportEnvProblemsResponse, error)
}

type Service struct {
	VariableDeclarations
	sdk *sdkversion.Gate

	mu        sync.Mutex
	resources []Resource
}

func NewService(variables VariableDeclarations) *Service {
	return &Service{VariableDeclarations: variables, sdk: sdkversion.NewGate(version.Version)}
}

func (s *Service) Declare(_ context.Context, req *resourcesv1.DeclareRequest) (*resourcesv1.DeclareResponse, error) {
	resource, err := Parse(req)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.resources = append(s.resources, resource)
	s.mu.Unlock()
	return &resourcesv1.DeclareResponse{}, nil
}

func (s *Service) Resources() []Resource {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Resource, len(s.resources))
	copy(out, s.resources)
	return out
}

func (s *Service) ForgetResources() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources = nil
}

func (s *Service) TakeSDKRefusal() error { return s.sdk.Take() }

func (s *Service) Handler() (string, http.Handler) {
	return resourcesv1connect.NewResourceServiceHandler(s, connect.WithInterceptors(validate.NewInterceptor(), s.sdk.Interceptor()))
}
