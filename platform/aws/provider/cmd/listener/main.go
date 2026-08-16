package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/platform/aws/provider/membrane/bucket"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
)

const (
	stateTableEnvVar     = "OCEL_RUNTIME_STATE_TABLE"
	sessionPrefixEnvVar  = "OCEL_RUNTIME_SESSION_PREFIX"
	allowedOriginsEnvVar = "OCEL_LISTENER_ALLOWED_ORIGINS"
)

func main() {
	listener, err := newListener(context.Background(), os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel: %v\n", err)
		os.Exit(1)
	}
	lambda.Start(listener.Handle)
}

func newListener(ctx context.Context, getenv func(string) string) (*bucket.Listener, error) {
	table := getenv(stateTableEnvVar)
	if table == "" {
		return nil, fmt.Errorf("%s is not set, so the sessions this bucket's uploads complete have nowhere to be read from", stateTableEnvVar)
	}
	prefix := getenv(sessionPrefixEnvVar)
	if prefix == "" {
		return nil, fmt.Errorf("%s is not set, so this listener would read a key space its role is not granted", sessionPrefixEnvVar)
	}
	cfg, err := sdkconfig.Runtime(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return bucket.NewListener(bucket.ListenerConfig{
		DDB:              dynamodb.NewFromConfig(cfg),
		Tagger:           s3.NewFromConfig(cfg),
		Table:            table,
		SessionKeyPrefix: prefix,
		AllowedOrigins:   allowedOrigins(getenv(allowedOriginsEnvVar)),
	}), nil
}

func allowedOrigins(raw string) []string {
	var out []string
	for _, origin := range strings.Split(raw, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			out = append(out, origin)
		}
	}
	return out
}
