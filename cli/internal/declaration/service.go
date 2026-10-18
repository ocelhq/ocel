package declaration

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/cli/internal/sdkversion"
	"github.com/ocelhq/ocel/cli/internal/version"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/resources/v1/resourcesv1connect"
	"google.golang.org/protobuf/proto"
)

type VariableDeclarations interface {
	DeclareEnv(context.Context, *resourcesv1.DeclareEnvRequest) (*resourcesv1.DeclareEnvResponse, error)
	ReportEnvProblems(context.Context, *resourcesv1.ReportEnvProblemsRequest) (*resourcesv1.ReportEnvProblemsResponse, error)
}

type Service struct {
	variables VariableDeclarations
	sdk       *sdkversion.Gate

	mu        sync.Mutex
	declared  []*resourcesv1.DeclareRequest
	resources []Resource

	envMu      sync.Mutex
	envAnswers []envAnswer
	keyFiles   map[string]string
	problems   []*resourcesv1.VariableProblem
}

type envAnswer struct {
	req *resourcesv1.DeclareEnvRequest
	res *resourcesv1.DeclareEnvResponse
}

var growsAsDeclared = map[resourcesv1.ResourceType]bool{
	resourcesv1.ResourceType_RESOURCE_TYPE_KV:       true,
	resourcesv1.ResourceType_RESOURCE_TYPE_REALTIME: true,
}

func NewService(variables VariableDeclarations) *Service {
	return &Service{variables: variables, sdk: sdkversion.NewGate(version.Version), keyFiles: map[string]string{}}
}

func (s *Service) Declare(_ context.Context, req *resourcesv1.DeclareRequest) (*resourcesv1.DeclareResponse, error) {
	resource, err := Parse(req)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if slices.ContainsFunc(s.declared, func(prior *resourcesv1.DeclareRequest) bool { return proto.Equal(prior, req) }) {
		return &resourcesv1.DeclareResponse{}, nil
	}
	s.declared = append(s.declared, req)
	for i, prior := range s.resources {
		if growsAsDeclared[resource.Type] && resource.Source != "" && prior.Source == resource.Source && prior.Type == resource.Type && prior.Name == resource.Name {
			s.resources[i] = resource
			return &resourcesv1.DeclareResponse{}, nil
		}
	}
	s.resources = append(s.resources, resource)
	return &resourcesv1.DeclareResponse{}, nil
}

func (s *Service) DeclareEnv(ctx context.Context, req *resourcesv1.DeclareEnvRequest) (*resourcesv1.DeclareEnvResponse, error) {
	s.envMu.Lock()
	defer s.envMu.Unlock()
	for _, answered := range s.envAnswers {
		if proto.Equal(answered.req, req) {
			return answered.res, nil
		}
	}
	if err := s.refuseKeyOfAnotherFile(req); err != nil {
		return nil, err
	}
	res, err := s.variables.DeclareEnv(ctx, req)
	if err != nil {
		return nil, err
	}
	for _, definition := range req.GetDefinitions() {
		s.keyFiles[definition.GetKey()] = declaringFile(definition.GetSource())
	}
	s.envAnswers = append(s.envAnswers, envAnswer{req: req, res: res})
	return res, nil
}

func (s *Service) refuseKeyOfAnotherFile(req *resourcesv1.DeclareEnvRequest) error {
	for _, definition := range req.GetDefinitions() {
		file := declaringFile(definition.GetSource())
		if claimed, ok := s.keyFiles[definition.GetKey()]; ok && claimed != file {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
				"'%s' is declared in %s and in %s: a key is defined by exactly one file", definition.GetKey(), sourceOrUnknown(claimed), sourceOrUnknown(file)))
		}
	}
	return nil
}

func declaringFile(source string) string {
	if colon := strings.LastIndex(source, ":"); colon > 0 {
		if _, err := strconv.Atoi(source[colon+1:]); err == nil {
			return source[:colon]
		}
	}
	return source
}

func sourceOrUnknown(source string) string {
	if source == "" {
		return "<unknown source>"
	}
	return source
}

func (s *Service) ReportEnvProblems(ctx context.Context, req *resourcesv1.ReportEnvProblemsRequest) (*resourcesv1.ReportEnvProblemsResponse, error) {
	s.envMu.Lock()
	defer s.envMu.Unlock()
	var fresh []*resourcesv1.VariableProblem
	for _, problem := range req.GetProblems() {
		if slices.ContainsFunc(s.problems, func(prior *resourcesv1.VariableProblem) bool { return proto.Equal(prior, problem) }) {
			continue
		}
		s.problems = append(s.problems, problem)
		fresh = append(fresh, problem)
	}
	if len(fresh) == 0 {
		return &resourcesv1.ReportEnvProblemsResponse{}, nil
	}
	return s.variables.ReportEnvProblems(ctx, &resourcesv1.ReportEnvProblemsRequest{Problems: fresh})
}

func (s *Service) Resources() []Resource {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Resource, len(s.resources))
	copy(out, s.resources)
	return out
}

func (s *Service) ForgetDeclarations() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.declared = nil
	s.resources = nil
	s.envMu.Lock()
	defer s.envMu.Unlock()
	s.envAnswers = nil
	s.keyFiles = map[string]string{}
	s.problems = nil
}

func (s *Service) TakeSDKRefusal() error { return s.sdk.Take() }

func (s *Service) Handler() (string, http.Handler) {
	return resourcesv1connect.NewResourceServiceHandler(s, connect.WithInterceptors(validate.NewInterceptor(), s.sdk.Interceptor()))
}
