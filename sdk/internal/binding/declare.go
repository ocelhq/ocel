package binding

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

func Discovering() bool {
	return os.Getenv(PhaseEnv) == discoveryPhase
}

func resources() resourcesv1connect.ResourceServiceClient {
	return resourcesv1connect.NewResourceServiceClient(
		http.DefaultClient,
		os.Getenv(DevServerEnv),
		connect.WithProtoJSON(),
		connect.WithInterceptors(authorizing),
	)
}

var authorizing = connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", Bearer(os.Getenv(DevServerTokenEnv)))
		req.Header().Set(sdkVersionHeader, "go/"+Version)
		return next(ctx, req)
	}
})

func CallSite(skip int) string {
	_, file, line, _ := runtime.Caller(skip + 2)
	return fmt.Sprintf("%s:%d", file, line)
}

func Declare(source string, req *resourcesv1.DeclareRequest) {
	if !Discovering() {
		return
	}
	req.Source = source
	if _, err := resources().Declare(context.Background(), req); err != nil {
		kind := strings.ToLower(strings.TrimPrefix(req.GetResource().GetType().String(), "RESOURCE_TYPE_"))
		panic(fmt.Sprintf("ocel: declare %s %q: %v", kind, req.GetResource().GetName(), err))
	}
}

func DeclareVariables(req *resourcesv1.DeclareEnvRequest) (*resourcesv1.DeclareEnvResponse, error) {
	return resources().DeclareEnv(context.Background(), req)
}

func ReportEnvProblems(req *resourcesv1.ReportEnvProblemsRequest) error {
	_, err := resources().ReportEnvProblems(context.Background(), req)
	return err
}
