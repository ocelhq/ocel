package providerprocess

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/cloud"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/run"
	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/cli/internal/wireerror"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/cost/v1/costv1connect"
	"github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1/variablestorev1connect"
)

const (
	versionMismatchPrefix = "cloud.version_mismatch:"

	remotePingAfter   = 30 * time.Second
	remotePingTimeout = 15 * time.Second
)

type Remote struct {
	Name       string
	URL        string
	Tokens     cloud.TokenSource
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
	t := &remoteTransport{
		clients: newClients(httpClient, strings.TrimRight(remote.URL, "/"), MaxMessageBytes, cloud.Headers{Tokens: remote.Tokens, Session: session}, wireerror.Interceptor(func(err *connect.Error) *clierror.Error {
			return cmp.Or(cloudRefusal(remote.URL, err), providerRefusal(err))
		})),
		http: httpClient,
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

func cloudRefusal(url string, err *connect.Error) *clierror.Error {
	switch {
	case err.Code() == connect.CodeFailedPrecondition && strings.HasPrefix(err.Message(), versionMismatchPrefix):
		return &clierror.Error{Code: clierror.CodeCloudUpgradeRequired, Cause: fmt.Errorf("ocel %s is a release Ocel Cloud at %s no longer serves — upgrade ocel and run this again: %s", version.Version, url, strings.TrimSpace(strings.TrimPrefix(err.Message(), versionMismatchPrefix)))}
	case err.Code() == connect.CodeUnauthenticated:
		return &clierror.Error{Code: clierror.CodeConsoleNotLoggedIn, Cause: fmt.Errorf("the token the console issued is not one Ocel Cloud at %s accepts — run `ocel login` and run this again: %s", url, err.Message())}
	}
	return nil
}
