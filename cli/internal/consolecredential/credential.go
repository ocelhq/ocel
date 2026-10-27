package consolecredential

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/githubactions"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	tokenFileName       = "token"
	refreshAhead        = 10 * time.Minute
	defaultRefreshFloor = 30 * time.Second
	firstRetryDelay     = time.Second
	maxRetryDelay       = time.Minute
	issueTimeout        = 30 * time.Second
)

var ErrNoTarget = errors.New("the console binds no target to this project's tier")

var finalCodes = []connect.Code{connect.CodeNotFound, connect.CodePermissionDenied, connect.CodeFailedPrecondition, connect.CodeUnauthenticated}

type Console struct {
	Getenv       func(string) string
	Version      string
	RefreshFloor time.Duration
}

type Request struct {
	Tier   environmentv1.Tier
	Vendor string
}

type Credential struct {
	issued    *consolev1.IssueCredentialResponse
	dir       string
	tokenFile string
	floor     time.Duration
	warn      func(string)

	mu      sync.Mutex
	lastErr error

	stop context.CancelFunc
	done chan struct{}
}

type refusedIssue struct {
	code    connect.Code
	message string
}

func (e *refusedIssue) Error() string { return "the console issued no credential: " + e.message }

func (c Console) IsRequested() bool {
	return c.Getenv != nil && githubactions.IsRunning(c.Getenv) && c.Getenv(console.ProjectIDEnvVar) != ""
}

func (c Console) IsWantedBy(taken *contractv1.WebIdentityFacts) bool {
	return taken != nil && len(provider.WebIdentityFacts{CredentialEnvVars: taken.GetCredentialEnvVars()}.FindOwnCredentialEnvVars(c.Getenv)) == 0
}

func (c Console) Issue(ctx context.Context, req Request, warn func(string)) (*Credential, error) {
	issue, err := c.issuing(req)
	if err != nil {
		return nil, err
	}
	issued, err := issue(ctx)
	var refused *refusedIssue
	if errors.As(err, &refused) && refused.code == connect.CodeNotFound {
		return nil, ErrNoTarget
	}
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "ocel-console-credential-")
	if err != nil {
		return nil, fmt.Errorf("make a private directory for the console's credential: %w", err)
	}
	credential := &Credential{
		issued: issued, dir: dir, tokenFile: filepath.Join(dir, tokenFileName),
		floor: c.refreshFloor(), warn: warn, done: make(chan struct{}),
	}
	if err := credential.write(issued.GetToken()); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	refreshing, stop := context.WithCancel(context.WithoutCancel(ctx))
	credential.stop = stop
	go credential.refresh(refreshing, issue)
	return credential, nil
}

func (c Console) refreshFloor() time.Duration {
	if c.RefreshFloor > 0 {
		return c.RefreshFloor
	}
	return defaultRefreshFloor
}

func (c Console) issuing(req Request) (func(context.Context) (*consolev1.IssueCredentialResponse, error), error) {
	apiURL := console.NormalizeBaseURL(c.Getenv(console.URLEnvVar))
	audience, err := console.Origin(apiURL)
	if err != nil {
		return nil, err
	}
	client := console.New(apiURL)
	request := &consolev1.IssueCredentialRequest{
		ProjectId:  c.Getenv(console.ProjectIDEnvVar),
		Tier:       req.Tier,
		Vendor:     req.Vendor,
		CliVersion: c.Version,
	}
	return func(ctx context.Context) (*consolev1.IssueCredentialResponse, error) {
		ctx, cancel := context.WithTimeout(ctx, issueTimeout)
		defer cancel()
		idToken, err := githubactions.RequestIDToken(ctx, c.Getenv, audience)
		if err != nil {
			return nil, err
		}
		issued, err := client.IssueCredential(ctx, idToken, request)
		var refused *connect.Error
		switch {
		case errors.As(err, &refused):
			return nil, &refusedIssue{code: refused.Code(), message: refused.Message()}
		case err != nil:
			return nil, fmt.Errorf("the console issued no credential: %w", err)
		case issued.GetExpiresAt() == nil || !issued.GetExpiresAt().AsTime().After(time.Now()):
			return nil, errors.New("the console issued a credential that has already expired or names no expiry")
		}
		return issued, nil
	}, nil
}

func (c *Credential) WebIdentity() *contractv1.WebIdentity {
	return &contractv1.WebIdentity{
		TokenFile:        c.tokenFile,
		IdentityProvider: c.issued.GetIdentity().GetIdentityProvider(),
		Principal:        c.issued.GetIdentity().GetPrincipal(),
		SessionName:      c.issued.GetSessionName(),
	}
}

func (c *Credential) Account() string { return c.issued.GetAccount() }

func (c *Credential) Region() string { return c.issued.GetRegion() }

func (c *Credential) Close() error {
	c.stop()
	<-c.done
	os.RemoveAll(c.dir)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

func (c *Credential) write(token string) error {
	staged, err := os.CreateTemp(c.dir, tokenFileName+"-*")
	if err != nil {
		return fmt.Errorf("write the console's credential: %w", err)
	}
	_, err = staged.WriteString(token)
	if closeErr := staged.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(staged.Name(), 0o600)
	}
	if err == nil {
		err = os.Rename(staged.Name(), c.tokenFile)
	}
	if err != nil {
		os.Remove(staged.Name())
		return fmt.Errorf("write the console's credential: %w", err)
	}
	return nil
}

func (c *Credential) refresh(ctx context.Context, issue func(context.Context) (*consolev1.IssueCredentialResponse, error)) {
	defer close(c.done)
	retry := firstRetryDelay
	wait := c.refreshDelay(time.Now(), c.issued.GetExpiresAt().AsTime())
	for {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		issued, err := issue(ctx)
		if err == nil {
			err = c.write(issued.GetToken())
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.fail(err)
			var refused *refusedIssue
			if errors.As(err, &refused) && isFinal(refused.code) {
				return
			}
			wait = jittered(retry)
			retry = min(2*retry, maxRetryDelay)
			continue
		}
		c.mu.Lock()
		c.lastErr = nil
		c.mu.Unlock()
		retry = firstRetryDelay
		wait = c.refreshDelay(time.Now(), issued.GetExpiresAt().AsTime())
	}
}

func (c *Credential) fail(err error) {
	c.mu.Lock()
	c.lastErr = err
	c.mu.Unlock()
	if c.warn != nil {
		c.warn(fmt.Sprintf("The console's credential could not be renewed: %v", err))
	}
}

func isFinal(code connect.Code) bool {
	return slices.Contains(finalCodes, code)
}

func (c *Credential) refreshDelay(issuedAt, expiresAt time.Time) time.Duration {
	lifetime := expiresAt.Sub(issuedAt)
	wait := lifetime - refreshAhead
	if lifetime < 2*refreshAhead {
		wait = lifetime / 2
	}
	return max(wait, c.floor)
}

func jittered(delay time.Duration) time.Duration {
	return delay/2 + rand.N(delay/2+1)
}
