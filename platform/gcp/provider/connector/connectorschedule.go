package connector

import (
	"context"
	"net/http"

	"google.golang.org/api/cloudscheduler/v1"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const connectorBeatSchedule = "* * * * *"

func buildConnectorSchedule(c *gcloud.Clients, url string) *cloudscheduler.Job {
	return &cloudscheduler.Job{
		Name:        gcloud.SchedulePath(c, c.Connector()),
		Description: "wakes the ocel connector once a minute to send the console a heartbeat",
		Schedule:    connectorBeatSchedule,
		TimeZone:    gcloud.ScheduleTimeZone,
		HttpTarget: &cloudscheduler.HttpTarget{
			Uri:        url + endpoint.HeartbeatPath,
			HttpMethod: http.MethodPost,
			OidcToken: &cloudscheduler.OidcToken{
				ServiceAccountEmail: c.ConnectorAccountEmail(),
				Audience:            url,
			},
		},
		RetryConfig: &cloudscheduler.RetryConfig{RetryCount: 0, ForceSendFields: []string{"RetryCount"}},
	}
}

func scheduleConnectorHeartbeat(ctx context.Context, c *gcloud.Clients, url string) error {
	return gcloud.PutSchedule(ctx, c, c.Connector(), buildConnectorSchedule(c, url))
}

func takeConnectorSchedule(ctx context.Context, c *gcloud.Clients, progress progress.Log) error {
	if err := gcloud.TakeSchedule(ctx, c, c.Connector()); err != nil {
		return err
	}
	gcloud.EnsureProgress(progress).Say("Deleted Cloud Scheduler job " + c.Connector() + ", which woke the connector for its heartbeat")
	return nil
}
