package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/membrane"
	"github.com/ocelhq/ocel/platform/aws/provider/membrane/bucket"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
)

const (
	runtimeAddressEnvVar = "OCEL_RUNTIME_ADDRESS"
	stateTableEnvVar     = "OCEL_RUNTIME_STATE_TABLE"
	sessionPrefixEnvVar  = "OCEL_RUNTIME_SESSION_PREFIX"
)

func membraneWanted(links []live.Link) bool {
	for _, l := range links {
		if naming.CrossesMembrane(l.Type) {
			return true
		}
	}
	return false
}

func serveMembrane(ctx context.Context, links []live.Link, table, sessionPrefix string) ([]string, <-chan error, error) {
	if !membraneWanted(links) {
		return nil, nil, nil
	}
	if table == "" {
		return nil, nil, fmt.Errorf("%s is not set, so the sessions this deployment's buckets keep have nowhere to live", stateTableEnvVar)
	}
	if sessionPrefix == "" {
		return nil, nil, fmt.Errorf("%s is not set, so this deployment's sessions would share a key space with every other deployment in the account", sessionPrefixEnvVar)
	}

	cfg, err := sdkconfig.Runtime(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("load aws config: %w", err)
	}
	svc := bucket.New(bucket.Config{
		DDB:              dynamodb.NewFromConfig(cfg),
		Presigner:        s3.NewPresignClient(s3.NewFromConfig(cfg)),
		Table:            table,
		SessionKeyPrefix: sessionPrefix,
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, fmt.Errorf("bind the membrane listener: %w", err)
	}
	token, err := membraneToken()
	if err != nil {
		ln.Close()
		return nil, nil, err
	}

	srv := &http.Server{Handler: membrane.NewMux(token, svc)}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	return []string{
		runtimeAddressEnvVar + "=http://" + ln.Addr().String(),
		channel.SessionTokenEnvVar + "=" + token,
	}, served, nil
}

func superviseMembrane(served <-chan error) {
	if served == nil {
		return
	}
	err := <-served
	fmt.Fprintf(os.Stderr, "ocel: the membrane stopped serving this deployment's links: %v\n", err)
	os.Exit(1)
}

func membraneToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("draw a membrane session token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
