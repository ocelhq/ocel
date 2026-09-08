package sdk

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

func declare(req *resourcesv1.DeclareRequest) error {
	client := resourcesv1connect.NewResourceServiceClient(
		http.DefaultClient,
		os.Getenv(devServerEnv),
		connect.WithProtoJSON(),
	)
	_, err := client.Declare(context.Background(), req)
	return err
}
