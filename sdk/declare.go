package ocel

import (
	"context"
	"net/http"
	"os"

	"connectrpc.com/connect"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/resources/v1/resourcesv1connect"
)

const (
	phaseEnv     = "OCEL_PHASE"
	devServerEnv = "OCEL_DEV_SERVER"

	discoveryPhase = "discovery"
)

func discovering() bool {
	return os.Getenv(phaseEnv) == discoveryPhase
}

func resources() resourcesv1connect.ResourceServiceClient {
	return resourcesv1connect.NewResourceServiceClient(
		http.DefaultClient,
		os.Getenv(devServerEnv),
		connect.WithProtoJSON(),
	)
}

func declare(req *resourcesv1.DeclareRequest) error {
	_, err := resources().Declare(context.Background(), req)
	return err
}

func declareVariables(req *resourcesv1.DeclareEnvRequest) (*resourcesv1.DeclareEnvResponse, error) {
	return resources().DeclareEnv(context.Background(), req)
}

func reportEnvProblems(req *resourcesv1.ReportEnvProblemsRequest) error {
	_, err := resources().ReportEnvProblems(context.Background(), req)
	return err
}
