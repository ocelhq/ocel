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
	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
	"github.com/ocelhq/ocel/platform/aws/runtime/bucket"
	"github.com/ocelhq/ocel/platform/aws/runtime/proxy"
)

const (
	stateTableEnvVar    = "OCEL_RUNTIME_STATE_TABLE"
	sessionPrefixEnvVar = "OCEL_RUNTIME_SESSION_PREFIX"
)

func proxyWanted(bindings []live.Binding) bool {
	for _, l := range bindings {
		if naming.Proxied(l.Type) {
			return true
		}
	}
	return false
}

func serveProxy(ctx context.Context, bindings []live.Binding, table, sessionPrefix string) ([]string, <-chan error, error) {
	if !proxyWanted(bindings) {
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
		return nil, nil, fmt.Errorf("bind the proxy listener: %w", err)
	}
	token, err := proxyToken()
	if err != nil {
		ln.Close()
		return nil, nil, err
	}

	srv := &http.Server{Handler: proxy.NewMux(token, svc)}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()

	return []string{
		constants.RuntimeAddressEnvName + "=http://" + ln.Addr().String(),
		channel.SessionTokenEnvVar + "=" + token,
	}, served, nil
}

func superviseProxy(served <-chan error) {
	if served == nil {
		return
	}
	err := <-served
	fmt.Fprintf(os.Stderr, "ocel: the proxy stopped serving this deployment's bindings: %v\n", err)
	os.Exit(1)
}

func proxyToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("draw a proxy session token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
