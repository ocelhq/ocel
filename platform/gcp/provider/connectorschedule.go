package gcp

import (
	"context"
	"net/http"

	"google.golang.org/api/cloudscheduler/v1"

	"github.com/ocelhq/ocel/pkg/connectorserver"
	"github.com/ocelhq/ocel/pkg/progress"
)

const connectorBeatSchedule = "* * * * *"

func (c *clients) buildConnectorSchedule(url string) *cloudscheduler.Job {
	return &cloudscheduler.Job{
		Name:        schedulePath(c, c.Connector()),
		Description: "wakes the ocel connector once a minute to send the console a heartbeat",
		Schedule:    connectorBeatSchedule,
		TimeZone:    scheduleTimeZone,
		HttpTarget: &cloudscheduler.HttpTarget{
			Uri:        url + connectorserver.HeartbeatPath,
			HttpMethod: http.MethodPost,
			OidcToken: &cloudscheduler.OidcToken{
				ServiceAccountEmail: c.ConnectorAccountEmail(),
				Audience:            url,
			},
		},
		RetryConfig: &cloudscheduler.RetryConfig{RetryCount: 0, ForceSendFields: []string{"RetryCount"}},
	}
}

func (c *clients) scheduleConnectorHeartbeat(ctx context.Context, url string) error {
	return c.putSchedule(ctx, c.Connector(), c.buildConnectorSchedule(url))
}

func (c *clients) takeConnectorSchedule(ctx context.Context, progress progress.Log) error {
	if err := c.takeSchedule(ctx, c.Connector()); err != nil {
		return err
	}
	ensureProgress(progress).Say("Deleted Cloud Scheduler job " + c.Connector() + ", which woke the connector for its heartbeat")
	return nil
}
