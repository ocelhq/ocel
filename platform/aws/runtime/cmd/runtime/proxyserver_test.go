package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/constants"
	"github.com/ocelhq/ocel/pkg/naming"
	blobv1 "github.com/ocelhq/ocel/pkg/proto/app/blob/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/blob/v1/blobv1connect"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/vars/live"
)

func proxyEnvValue(t *testing.T, env []string, key string) string {
	t.Helper()
	for _, entry := range env {
		if name, value, ok := strings.Cut(entry, "="); ok && name == key {
			return value
		}
	}
	t.Fatalf("env %q carries no %s", env, key)
	return ""
}

var testSessionPrefix = naming.SessionKeyPrefix("shop", "prod")

func TestServeProxy(t *testing.T) {
	t.Run("a deployment whose links all go direct serves nothing", func(t *testing.T) {
		env, served, err := serveProxy(context.Background(), []live.Link{{Name: "db--main", Type: linksv1.LinkType_LINK_TYPE_POSTGRES}}, "state", testSessionPrefix)
		if err != nil {
			t.Fatalf("serveProxy: %v", err)
		}
		if env != nil || served != nil {
			t.Fatalf("serveProxy = %q, want no proxy for a deployment that reaches postgres directly", env)
		}
	})

	t.Run("a bucket with nowhere to keep its sessions fails by name", func(t *testing.T) {
		_, _, err := serveProxy(context.Background(), []live.Link{{Name: "bucket--uploads", Type: linksv1.LinkType_LINK_TYPE_BUCKET}}, "", testSessionPrefix)
		if err == nil || !strings.Contains(err.Error(), stateTableEnvVar) {
			t.Fatalf("serveProxy err = %v, want it to name %s", err, stateTableEnvVar)
		}
	})

	t.Run("a bucket whose sessions have no key scope refuses to serve", func(t *testing.T) {
		_, _, err := serveProxy(context.Background(), []live.Link{{Name: "bucket--uploads", Type: linksv1.LinkType_LINK_TYPE_BUCKET}}, "state", "")
		if err == nil || !strings.Contains(err.Error(), sessionPrefixEnvVar) {
			t.Fatalf("serveProxy err = %v, want it to name %s", err, sessionPrefixEnvVar)
		}
	})

	t.Run("a bucket is served in-process, reachable only with the token the child is handed", func(t *testing.T) {
		t.Setenv("AWS_REGION", "us-east-1")
		t.Setenv("AWS_ACCESS_KEY_ID", "test")
		t.Setenv("AWS_SECRET_ACCESS_KEY", "test")

		env, served, err := serveProxy(context.Background(), []live.Link{{Name: "bucket--uploads", Type: linksv1.LinkType_LINK_TYPE_BUCKET}}, "state", testSessionPrefix)
		if err != nil {
			t.Fatalf("serveProxy: %v", err)
		}
		if served == nil {
			t.Fatal("serveProxy returned no channel to carry the proxy's terminal error")
		}

		addr := proxyEnvValue(t, env, constants.RuntimeAddressEnvName)
		if !strings.HasPrefix(addr, "http://127.0.0.1:") {
			t.Fatalf("%s = %q, want a loopback address the sandbox alone can reach", constants.RuntimeAddressEnvName, addr)
		}
		token := proxyEnvValue(t, env, channel.SessionTokenEnvVar)
		if token == "" {
			t.Fatalf("%s is empty, so the proxy is open to anything in the sandbox", channel.SessionTokenEnvVar)
		}

		client := blobv1connect.NewBucketServiceClient(http.DefaultClient, addr)
		_, err = client.PresignUpload(context.Background(), &blobv1.PresignUploadRequest{Bucket: "uploads"})
		var connectErr *connect.Error
		if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnauthenticated {
			t.Fatalf("unauthenticated PresignUpload err = %v, want CodeUnauthenticated", err)
		}

		bearer := blobv1connect.NewBucketServiceClient(&http.Client{Transport: bearerToken(token)}, addr)
		_, err = bearer.PresignUpload(context.Background(), &blobv1.PresignUploadRequest{Bucket: "uploads"})
		if errors.As(err, &connectErr) && connectErr.Code() == connect.CodeUnauthenticated {
			t.Fatalf("PresignUpload with the token exported into the child's environment = %v, want the proxy to answer it", err)
		}
	})
}

type bearerToken string

func (b bearerToken) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", channel.FormatAuthHeader(string(b)))
	return http.DefaultTransport.RoundTrip(req)
}
