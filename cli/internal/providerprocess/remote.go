package providerprocess

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/version"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
)

const (
	VersionHeader = "Ocel-Version"
	SessionHeader = "Ocel-Session"

	versionMismatchPrefix = "cloud.version_mismatch:"

	remotePingAfter   = 30 * time.Second
	remotePingTimeout = 15 * time.Second
)

type TokenSource interface {
	Ensure(ctx context.Context) (string, error)
}

type Remote struct {
	Name       string
	URL        string
	Tokens     TokenSource
	HTTPClient *http.Client
}

type remoteTransport struct {
	clients clients
	http    *http.Client
	facts   *contractv1.ProviderFacts
}

func StartRemote(ctx context.Context, cfg *project.Project, span *run.Span, questions Questions, remote Remote) (*Provider, error) {
	if len(cfg.Transforms) > 0 {
		return nil, &clierror.Error{Code: clierror.CodeCloudUnsupported, Cause: fmt.Errorf("%s applies transforms, and Ocel Cloud runs none: drop them from its config, or name a provider to run them against your own account", cfg.Path)}
	}
	config, err := newProviderConfig(cfg, nil)
	if err != nil {
		return nil, err
	}
	session, err := newSessionID()
	if err != nil {
		return nil, err
	}
	httpClient := remote.HTTPClient
	if httpClient == nil {
		httpClient = newRemoteHTTPClient()
	}
	headed := &http.Client{
		Transport:     sessionHeaders{next: transportOf(httpClient), tokens: remote.Tokens, session: session},
		CheckRedirect: httpClient.CheckRedirect,
		Jar:           httpClient.Jar,
	}
	t := &remoteTransport{
		clients: newClients(headed, strings.TrimRight(remote.URL, "/"), MaxMessageBytes, remoteErrors{url: remote.URL}),
		http:    headed,
	}
	if t.facts, err = configure(ctx, t.clients.provider, remote.Name, config, cfg.EdgeSelection()); err != nil {
		return nil, err
	}
	questions.run = span.Run()
	return &Provider{span: span, questions: questions, name: remote.Name, transport: t}, nil
}

func newRemoteHTTPClient() *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.HTTP2 = &http.HTTP2Config{SendPingTimeout: remotePingAfter, PingTimeout: remotePingTimeout}
	return &http.Client{Transport: base}
}

func transportOf(c *http.Client) http.RoundTripper {
	if c.Transport == nil {
		return http.DefaultTransport
	}
	return c.Transport
}

func newSessionID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("mint an Ocel Cloud session id: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

func (t *remoteTransport) Client() (contractv1connect.ProviderServiceClient, error) {
	return t.clients.provider, nil
}

func (t *remoteTransport) VariableStore() (variablestorev1connect.VariableStoreServiceClient, error) {
	return t.clients.variableStore, nil
}

func (t *remoteTransport) Cost() (costv1connect.CostServiceClient, error) {
	return t.clients.cost, nil
}

func (t *remoteTransport) Facts() *contractv1.ProviderFacts { return t.facts }

func (t *remoteTransport) explainLost(err error) error { return err }

func (t *remoteTransport) Close() { t.http.CloseIdleConnections() }

type sessionHeaders struct {
	next    http.RoundTripper
	tokens  TokenSource
	session string
}

func (h sessionHeaders) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := h.tokens.Ensure(req.Context())
	if err != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, fmt.Errorf("issue a token for Ocel Cloud: %w", err)
	}
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(VersionHeader, version.Version)
	req.Header.Set(SessionHeader, h.session)
	return h.next.RoundTrip(req)
}

type remoteErrors struct{ url string }

func (r remoteErrors) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		resp, err := next(ctx, req)
		return resp, refusalIn(r.classify(err))
	}
}

func (r remoteErrors) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		return classifiedConn{StreamingClientConn: next(ctx, spec), errors: r}
	}
}

func (remoteErrors) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}

func (r remoteErrors) classify(err error) error {
	var rpcErr *connect.Error
	if !errors.As(err, &rpcErr) {
		return err
	}
	switch {
	case rpcErr.Code() == connect.CodeFailedPrecondition && strings.HasPrefix(rpcErr.Message(), versionMismatchPrefix):
		return &clierror.Error{Code: clierror.CodeCloudUpgradeRequired, Cause: fmt.Errorf("ocel %s is a release Ocel Cloud at %s no longer serves — upgrade ocel and run this again: %s", version.Version, r.url, strings.TrimSpace(strings.TrimPrefix(rpcErr.Message(), versionMismatchPrefix)))}
	case rpcErr.Code() == connect.CodeUnauthenticated:
		return &clierror.Error{Code: clierror.CodeConsoleNotLoggedIn, Cause: fmt.Errorf("the token the console issued is not one Ocel Cloud at %s accepts — run `ocel login` and run this again: %s", r.url, rpcErr.Message())}
	}
	return err
}

type classifiedConn struct {
	connect.StreamingClientConn
	errors remoteErrors
}

func (c classifiedConn) Send(msg any) error {
	return c.errors.classify(c.StreamingClientConn.Send(msg))
}

func (c classifiedConn) Receive(msg any) error {
	return c.errors.classify(c.StreamingClientConn.Receive(msg))
}

func (c classifiedConn) CloseResponse() error {
	return c.errors.classify(c.StreamingClientConn.CloseResponse())
}
