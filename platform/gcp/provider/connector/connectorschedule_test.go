package connector

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestTheConnectorsHeartbeatScheduleCallsItEveryMinuteWithAnIDTokenForItsOwnAccount(t *testing.T) {
	t.Parallel()

	c := &gcloud.Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}
	url := googletest.ServedAt(c.Connector())
	schedule := buildConnectorSchedule(c, url)
	if schedule.Name != "projects/acme-prod/locations/europe-west1/jobs/ocel-connector" {
		t.Errorf("the schedule is named %q, want it beside the connector's service in its region", schedule.Name)
	}
	if schedule.Schedule != "* * * * *" || schedule.TimeZone != "Etc/UTC" {
		t.Errorf("the schedule fires on %q in %q, want every minute: the console reads a connector silent for ninety seconds as offline", schedule.Schedule, schedule.TimeZone)
	}
	if schedule.HttpTarget.HttpMethod != http.MethodPost || schedule.HttpTarget.Uri != url+"/v1/heartbeat" {
		t.Errorf("the schedule calls %s %s, want POST on the connector's heartbeat route", schedule.HttpTarget.HttpMethod, schedule.HttpTarget.Uri)
	}
	token := schedule.HttpTarget.OidcToken
	if token == nil || token.ServiceAccountEmail != "ocel-connector@acme-prod.iam.gserviceaccount.com" || token.Audience != url {
		t.Errorf("the schedule logs in as %+v, want an ID token for the connector's own account with the service's URL as its audience: that is all the connector admits", token)
	}
	if schedule.HttpTarget.OauthToken != nil {
		t.Errorf("the schedule sends an OAuth token %+v, and the connector verifies an ID token", schedule.HttpTarget.OauthToken)
	}
	if schedule.RetryConfig == nil || schedule.RetryConfig.RetryCount != 0 || !slices.Contains(schedule.RetryConfig.ForceSendFields, "RetryCount") {
		t.Errorf("the schedule retries %+v, want an explicit 0: the next minute is the retry", schedule.RetryConfig)
	}
}

func TestTheConnectorsHeartbeatScheduleIsCreatedMendedAndTakenThroughCloudScheduler(t *testing.T) {
	t.Parallel()

	server := googletest.NewSync()
	c := server.Open(t)
	ctx := context.Background()
	url := googletest.ServedAt(c.Connector())
	path := gcloud.SchedulePath(c, c.Connector())

	if err := scheduleConnectorHeartbeat(ctx, c, url); err != nil {
		t.Fatalf("scheduleConnectorHeartbeat() = %v", err)
	}
	if created := server.Schedule(path); created == nil || created.HttpTarget.Uri != url+"/v1/heartbeat" {
		t.Fatalf("the schedule is %s, want one calling the connector's heartbeat route", googletest.Encoded(t, created))
	}

	server.Change(func() { server.Schedules[path].State = "PAUSED" })
	if err := scheduleConnectorHeartbeat(ctx, c, url); err != nil {
		t.Fatalf("scheduleConnectorHeartbeat() over a paused schedule = %v, want a reinstall to mend it", err)
	}
	if !server.WasPatched(path) || server.Schedule(path).State != "ENABLED" {
		t.Errorf("the schedule after a reinstall is %s, want it patched and calling again", googletest.Encoded(t, server.Schedule(path)))
	}

	if err := takeConnectorSchedule(ctx, c, nil); err != nil {
		t.Fatalf("takeConnectorSchedule() = %v", err)
	}
	if server.Schedule(path) != nil {
		t.Error("the schedule outlived the connector, and it would call a service that is gone every minute")
	}
	if err := takeConnectorSchedule(ctx, c, nil); err != nil {
		t.Errorf("takeConnectorSchedule() of one already gone = %v, want nothing to do", err)
	}
}
