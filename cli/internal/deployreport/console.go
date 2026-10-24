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

var errNoTarget = errors.New("the provider named no target account it deployed to")

type Console struct {
	LoadCredentials func() (console.Credentials, error)
	Timeout         time.Duration
}

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

func (c Console) send(ctx context.Context, projectDir, subject string, stderr io.Writer, call func(ctx context.Context, client *console.Client, accessToken, projectID string) error) {
	credentials, err := c.loadCredentials()
	if errors.Is(err, console.ErrNotLoggedIn) {
		linked, err := console.IsLinked(projectDir)
		if err != nil {
			fmt.Fprintf(stderr, "Couldn't report %s to the console: %v\n", subject, err)
			return
		}
		if !linked {
			return
		}
		fmt.Fprintf(stderr, "Couldn't report %s to the console: not logged in. Run `ocel login`.\n", subject)
		return
	}
	if err != nil {
		fmt.Fprintf(stderr, "Couldn't report %s to the console: %v\n", subject, err)
		return
	}
	apiURL := console.BaseURL(credentials.APIURL)
	link, err := console.ReadLink(projectDir, apiURL)
	if err != nil {
		fmt.Fprintf(stderr, "Couldn't report %s to the console: %v\n", subject, err)
		return
	}
	if link == nil {
		fmt.Fprintln(stderr, "Not reported to the console: run `ocel link`.")
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cmp.Or(c.Timeout, DefaultTimeout))
	defer cancel()
	if err := call(ctx, console.New(apiURL), credentials.AccessToken, link.ProjectID); err != nil {
		fmt.Fprintf(stderr, "Couldn't report %s to the console: %v\n", subject, err)
	}
}

func (c Console) loadCredentials() (console.Credentials, error) {
	if c.LoadCredentials == nil {
		return console.Credentials{}, console.ErrNotLoggedIn
	}
	return c.LoadCredentials()
}
