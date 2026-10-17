package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/console"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	"github.com/ocelhq/ocel/pkg/proto/cloud/v1/cloudv1connect"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

const (
	ConnectorTarget = "ocel/cloud"
	ProviderName    = "ocel"
	Compute         = "microvm"
	MaxAppNameBytes = 40

	connectorVendor = "ocel"

	callTimeout                 = 30 * time.Second
	deployTimeout               = 15 * time.Minute
	slowestUploadBytesPerSecond = 256 << 10
	tokenMargin                 = time.Minute
)

var retryPolicy = provider.RetryPolicy{MaxAttempts: 5, MinDelay: time.Second, MaxDelay: 30 * time.Second}

func RefuseAppName(name string) error {
	if len(name) <= MaxAppNameBytes {
		return nil
	}
	return &clierror.Error{
		Code: clierror.CodeProjectInvalidConfig,
		Hint: "slug",
		Cause: fmt.Errorf("slug %q is %d characters, and Ocel Cloud runs a project under its slug in at most %d: shorten the slug",
			name, len(name), MaxAppNameBytes),
	}
}

type Cloud struct {
	url        string
	console    *console.Client
	session    string
	tokens     *tokenSource
	apps       cloudv1connect.AppServiceClient
	deploys    cloudv1connect.DeployServiceClient
	connectors cloudv1connect.ConnectorServiceClient
	pause      func(context.Context, time.Duration) error
}

func New(cloudURL string, consoleClient *console.Client, session string) *Cloud {
	cloudURL = strings.TrimRight(cloudURL, "/")
	tokens := &tokenSource{console: consoleClient, session: session}
	authorized := attachCloudToken(tokens)
	return &Cloud{
		url:        cloudURL,
		console:    consoleClient,
		session:    session,
		tokens:     tokens,
		apps:       cloudv1connect.NewAppServiceClient(http.DefaultClient, cloudURL, authorized),
		deploys:    cloudv1connect.NewDeployServiceClient(http.DefaultClient, cloudURL, authorized),
		connectors: cloudv1connect.NewConnectorServiceClient(http.DefaultClient, cloudURL, authorized),
		pause:      pause,
	}
}

type Deployed struct {
	Deployment *cloudv1.Deployment
	URL        string
	URLs       map[string]string
	Images     map[string]string
}

func (c *Cloud) Deploy(ctx context.Context, app string, manifest *contractv1.Manifest, images map[string]string, progress io.Writer) (Deployed, error) {
	claimed, err := c.claim(ctx, app)
	if err != nil {
		return Deployed{}, err
	}
	if err := c.ensureConnector(ctx, claimed.GetConnectorId()); err != nil {
		return Deployed{}, err
	}
	digests := make(map[string]string, len(images))
	for _, name := range slices.Sorted(maps.Keys(images)) {
		fmt.Fprintf(progress, "Uploading the image of %s\n", name)
		if digests[name], err = c.upload(ctx, images[name]); err != nil {
			return Deployed{}, err
		}
	}
	deployCtx, cancel := context.WithTimeout(ctx, deployTimeout)
	defer cancel()
	deployed, err := c.deploys.Deploy(deployCtx, &cloudv1.DeployRequest{App: app, Manifest: manifest, Images: digests})
	if err != nil {
		return Deployed{Images: digests}, err
	}
	return Deployed{Deployment: deployed.GetDeployment(), URL: deployed.GetUrl(), URLs: deployed.GetUrls(), Images: digests}, nil
}

func (c *Cloud) claim(ctx context.Context, app string) (*cloudv1.App, error) {
	var claimed *cloudv1.CreateAppResponse
	err := c.retry(ctx, callTimeout, func(ctx context.Context) (err error) {
		claimed, err = c.apps.CreateApp(ctx, &cloudv1.CreateAppRequest{Name: app})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("claim %s on Ocel Cloud: %w", app, err)
	}
	return claimed.GetApp(), nil
}

func (c *Cloud) ensureConnector(ctx context.Context, servedID string) error {
	connector, err := c.console.FindConnector(ctx, c.session, ConnectorTarget)
	if err != nil {
		return fmt.Errorf("read the console's connectors: %w", err)
	}
	if connector != nil && connector.GetId() == servedID && connector.GetUrl() != "" && connector.GetPublicKey() != "" {
		return nil
	}
	if connector == nil {
		if connector, err = c.console.UpsertConnector(ctx, c.session, ConnectorTarget, connectorVendor); err != nil {
			return fmt.Errorf("register Ocel Cloud as a connector in the console: %w", err)
		}
	}
	var installed *cloudv1.InstallConnectorResponse
	err = c.retry(ctx, callTimeout, func(ctx context.Context) (err error) {
		installed, err = c.connectors.InstallConnector(ctx, &cloudv1.InstallConnectorRequest{ConnectorId: connector.GetId()})
		return err
	})
	if err != nil {
		return fmt.Errorf("install the console's connector in Ocel Cloud: %w", err)
	}
	if connector.GetUrl() == installed.GetUrl() && connector.GetPublicKey() == installed.GetPublicKey() {
		return nil
	}
	_, err = c.console.SetConnectorAddress(ctx, c.session, connector.GetId(), console.ConnectorAddress{
		URL:       installed.GetUrl(),
		PublicKey: installed.GetPublicKey(),
		Compute:   Compute,
	})
	if err != nil {
		return fmt.Errorf("give the console Ocel Cloud's connector address: %w", err)
	}
	return nil
}

func (c *Cloud) upload(ctx context.Context, path string) (string, error) {
	digest, size, err := hashImage(path)
	if err != nil {
		return "", err
	}
	uploadTimeout := time.Minute + time.Duration(size/slowestUploadBytesPerSecond)*time.Second
	if err := c.retry(ctx, uploadTimeout, func(ctx context.Context) error { return c.put(ctx, path, digest, size) }); err != nil {
		return "", err
	}
	return digest, nil
}

func hashImage(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, fmt.Errorf("read %s: %w", path, err)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), size, nil
}

type uploadRefusal struct {
	digest     string
	status     int
	said       string
	retryAfter time.Duration
}

func (e *uploadRefusal) Error() string {
	return fmt.Sprintf("upload %s to Ocel Cloud: %d %s: %s", e.digest, e.status, http.StatusText(e.status), e.said)
}

func (c *Cloud) put(ctx context.Context, path, digest string, size int64) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	token, err := c.tokens.ensureValid(ctx)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, c.url+"/v1/images/"+digest, file)
	if err != nil {
		return err
	}
	request.ContentLength = size
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("upload %s to Ocel Cloud: %w", digest, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNoContent {
		return nil
	}
	said, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return &uploadRefusal{
		digest:     digest,
		status:     response.StatusCode,
		said:       strings.TrimSpace(string(said)),
		retryAfter: readRetryAfter(response.Header),
	}
}

func (c *Cloud) retry(ctx context.Context, timeout time.Duration, call func(context.Context) error) error {
	for attempt := 1; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		err := call(attemptCtx)
		cancel()
		if err == nil || !isTransient(err) || attempt == retryPolicy.MaxAttempts || ctx.Err() != nil {
			return err
		}
		wait := max(runs.Backoff(retryPolicy, attempt, runs.Jitter()), min(findRetryAfter(err), retryPolicy.MaxDelay))
		if err := c.pause(ctx, wait); err != nil {
			return err
		}
	}
}

func isTransient(err error) bool {
	var refused *uploadRefusal
	if errors.As(err, &refused) {
		return refused.status == http.StatusTooManyRequests || refused.status >= http.StatusInternalServerError
	}
	var answered *connect.Error
	if errors.As(err, &answered) {
		switch answered.Code() {
		case connect.CodeUnavailable, connect.CodeAborted, connect.CodeDeadlineExceeded:
			return true
		}
		return false
	}
	var unreached *url.Error
	return errors.As(err, &unreached)
}

func findRetryAfter(err error) time.Duration {
	var refused *uploadRefusal
	if errors.As(err, &refused) {
		return refused.retryAfter
	}
	var answered *connect.Error
	if errors.As(err, &answered) {
		return readRetryAfter(answered.Meta())
	}
	return 0
}

func readRetryAfter(header http.Header) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(header.Get("Retry-After")))
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func pause(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type tokenSource struct {
	console *console.Client
	session string

	mu   sync.Mutex
	held console.CloudToken
}

func (t *tokenSource) ensureValid(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if time.Until(t.held.ExpiresAt) > tokenMargin {
		return t.held.Token, nil
	}
	issued, err := t.console.IssueCloudToken(ctx, t.session)
	if err != nil {
		return "", err
	}
	t.held = issued
	return issued.Token, nil
}

func attachCloudToken(tokens *tokenSource) connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, request connect.AnyRequest) (connect.AnyResponse, error) {
			token, err := tokens.ensureValid(ctx)
			if err != nil {
				return nil, err
			}
			request.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, request)
		}
	}))
}
