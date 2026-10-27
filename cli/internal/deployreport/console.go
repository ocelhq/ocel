package deployreport

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ocelhq/ocel/cli/internal/console"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
)

const DefaultTimeout = 10 * time.Second

var (
	errNoTarget    = errors.New("the provider named no target account it deployed to")
	errNotLoggedIn = errors.New("you're not logged in; run `ocel login`")
)

type Console struct {
	LoadCredentials func() (console.Credentials, error)
	Getenv          func(string) string
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

func (c Console) ReportEnvironmentEvent(ctx context.Context, projectDir string, event *consolev1.EnvironmentEvent, stderr io.Writer) {
	c.send(ctx, projectDir, "environment event "+event.GetId(), stderr, func(ctx context.Context, client *console.Client, accessToken, projectID string) error {
		return client.RecordEnvironmentEvent(ctx, accessToken, projectID, event)
	})
}

func (c Console) send(ctx context.Context, projectDir, subject string, stderr io.Writer, call consoleCall) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cmp.Or(c.Timeout, DefaultTimeout))
	defer cancel()
	var session *reportSession
	var err error
	if isGitHubActions(c.getenv) && c.getenv(console.ProjectIDEnvVar) != "" {
		session, err = c.readPipelineSession(ctx)
	} else {
		session, err = c.readSavedLoginSession(projectDir)
	}
	if errors.Is(err, console.ErrNotLinked) {
		fmt.Fprintln(stderr, "This directory isn't linked to a console project, so nothing was reported there. Run `ocel link` to link it.")
		return
	}
	if err == nil && session != nil {
		err = call(ctx, console.New(session.apiURL), session.accessToken, session.projectID)
	}
	if err != nil {
		fmt.Fprintf(stderr, "Couldn't report %s to the console: %v\n", subject, err)
	}
}

func (c Console) readPipelineSession(ctx context.Context) (*reportSession, error) {
	apiURL := console.NormalizeBaseURL(c.getenv(console.URLEnvVar))
	audience, err := readAudience(apiURL)
	if err != nil {
		return nil, err
	}
	idToken, err := requestIDToken(ctx, c.getenv, audience)
	if err != nil {
		return nil, err
	}
	return &reportSession{apiURL: apiURL, accessToken: idToken, projectID: c.getenv(console.ProjectIDEnvVar)}, nil
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

func (c Console) getenv(name string) string {
	if c.Getenv == nil {
		return ""
	}
	return c.Getenv(name)
}

func (c Console) loadCredentials() (console.Credentials, error) {
	if c.LoadCredentials == nil {
		return console.Credentials{}, console.ErrNotLoggedIn
	}
	return c.LoadCredentials()
}
