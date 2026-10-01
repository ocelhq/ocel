package ocel

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"

	"connectrpc.com/connect"
	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
	"ocel.dev/internal/proto/app/resources/v1/resourcesv1connect"
)

const (
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
		connect.WithInterceptors(authorizing),
	)
}

var authorizing = connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", bearer(os.Getenv(devServerTokenEnv)))
		req.Header().Set(sdkVersionHeader, "go/"+version)
		return next(ctx, req)
	}
})

func readCallSite() string {
	_, file, line, _ := runtime.Caller(2)
	return fmt.Sprintf("%s:%d", file, line)
}

func declareResource(source string, req *resourcesv1.DeclareRequest) {
	if !discovering() {
		return
	}
	req.Source = source
	if err := declare(req); err != nil {
		kind := strings.ToLower(strings.TrimPrefix(req.GetResource().GetType().String(), "RESOURCE_TYPE_"))
		panic(fmt.Sprintf("ocel: declare %s %q: %v", kind, req.GetResource().GetName(), err))
	}
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
