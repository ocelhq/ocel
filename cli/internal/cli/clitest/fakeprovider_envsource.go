package clitest

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"

	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/provider/envvars/v1"
)

func (s *deployFakeProviderServer) SyncEnvSource(context.Context, *envvarsv1.SyncEnvSourceRequest) (*envvarsv1.SyncEnvSourceResponse, error) {
	return &envvarsv1.SyncEnvSourceResponse{Status: &envvarsv1.EnvSourceStatus{EnvSource: "builtin"}}, nil
}

func (s *deployFakeProviderServer) DescribeEnvSource(context.Context, *envvarsv1.DescribeEnvSourceRequest) (*envvarsv1.DescribeEnvSourceResponse, error) {
	return &envvarsv1.DescribeEnvSourceResponse{Status: &envvarsv1.EnvSourceStatus{EnvSource: "builtin"}}, nil
}

func (s *deployFakeProviderServer) PutEnvSourceValue(context.Context, *envvarsv1.PutEnvSourceValueRequest) (*envvarsv1.PutEnvSourceValueResponse, error) {
	return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this fake provider holds no writable env source"))
}
