package deployreport

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"slices"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/jobcontext"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

const (
	DefaultTimeout   = 10 * time.Second
	reportAttempts   = 4
	firstReportPause = 250 * time.Millisecond
)

var retriedCodes = []connect.Code{connect.CodeUnavailable, connect.CodeDeadlineExceeded, connect.CodeResourceExhausted, connect.CodeAborted}

var (
	errNoTarget    = errors.New("the provider named no target account it deployed to")
	errNotLoggedIn = errors.New("you're not logged in; run `ocel login`")
)

type Console struct {
	LoadCredentials func() (console.Credentials, error)
	Context         *jobcontext.Context
	Timeout         time.Duration
}

type reportSession struct {
	apiURL      string
	accessToken string
	projectID   string
}

type consoleCall func(ctx context.Context, client *console.Client, accessToken, projectID string) error

func (c Console) ReportDeployment(ctx context.Context, projectDir string, deployment *consolev1.Deployment, stderr io.Writer) {
	if deployment == nil {
		return
	}
	c.send(ctx, projectDir, "deployment "+deployment.GetId(), stderr, func(ctx context.Context, client *console.Client, accessToken, projectID string) error {
		if deployment.GetTarget() == "" {
			return errNoTarget
		}
		return client.ReportDeployment(ctx, accessToken, projectID, deployment)
	})
}

func (c Console) StartDeployment(ctx context.Context, projectDir string, start *consolev1.DeploymentStart) {
	if start.GetTarget() == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, cmp.Or(c.Timeout, DefaultTimeout))
	defer cancel()
	session, err := c.readSession(ctx, projectDir)
	if err != nil || session == nil {
		return
	}
	client := console.New(session.apiURL)
	_ = retrying(ctx, func() error { return client.StartDeployment(ctx, session.accessToken, session.projectID, start) })
}

func (c Console) ReportEnvironmentEvent(ctx context.Context, projectDir string, event *consolev1.EnvironmentEvent, stderr io.Writer) {
	c.send(ctx, projectDir, "environment event "+event.GetId(), stderr, func(ctx context.Context, client *console.Client, accessToken, projectID string) error {
		return client.RecordEnvironmentEvent(ctx, accessToken, projectID, event)
	})
}

func (c Console) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cmp.Or(c.Timeout, DefaultTimeout))
}

func (c Console) readSession(ctx context.Context, projectDir string) (*reportSession, error) {
	if c.Context != nil {
		return c.readContextSession(ctx)
	}
	return c.readSavedLoginSession(projectDir)
}

func (c Console) send(ctx context.Context, projectDir, subject string, stderr io.Writer, call consoleCall) {
	ctx, cancel := c.bounded(ctx)
	defer cancel()
	session, err := c.readSession(ctx, projectDir)
	if errors.Is(err, console.ErrNotLinked) {
		fmt.Fprintln(stderr, "This directory isn't linked to a console project, so nothing was reported there. Run `ocel link` to link it.")
		return
	}
	if err == nil && session != nil {
		client := console.New(session.apiURL)
		err = retrying(ctx, func() error { return call(ctx, client, session.accessToken, session.projectID) })
	}
	if err != nil {
		fmt.Fprintf(stderr, "Couldn't report %s to the console: %v\n", subject, err)
	}
}

func retrying(ctx context.Context, call func() error) error {
	pause := firstReportPause
	for attempt := 1; ; attempt++ {
		err := call()
		if err == nil || attempt == reportAttempts || !slices.Contains(retriedCodes, connect.CodeOf(err)) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(pause/2 + rand.N(pause/2)):
		}
		pause *= 2
	}
}

func (c Console) readContextSession(ctx context.Context) (*reportSession, error) {
	bearer, err := c.Context.Bearer(ctx)
	if err != nil {
		return nil, err
	}
	return &reportSession{apiURL: c.Context.ConsoleURL, accessToken: bearer, projectID: c.Context.ProjectID}, nil
}

func (c Console) readSavedLoginSession(projectDir string) (*reportSession, error) {
	credentials, err := c.loadCredentials()
	if errors.Is(err, console.ErrNotLoggedIn) {
		linked, err := console.IsLinked(projectDir)
		if err != nil || !linked {
			return nil, err
		}
		return nil, errNotLoggedIn
	}
	if err != nil {
		return nil, fmt.Errorf("your saved login could not be read: %w", err)
	}
	apiURL := console.BaseURL(credentials.APIURL)
	link, err := console.ReadLink(projectDir, apiURL)
	if err != nil {
		return nil, err
	}
	if link == nil {
		return nil, console.ErrNotLinked
	}
	return &reportSession{apiURL: apiURL, accessToken: credentials.AccessToken, projectID: link.ProjectID}, nil
}

func (c Console) loadCredentials() (console.Credentials, error) {
	if c.LoadCredentials == nil {
		return console.Credentials{}, console.ErrNotLoggedIn
	}
	return c.LoadCredentials()
}
