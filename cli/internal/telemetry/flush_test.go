package telemetry_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clitest/confighome"
	"github.com/ocelhq/ocel/cli/internal/telemetry"
)

func withBuildValues(t *testing.T, key, endpoint string) {
	t.Helper()
	previousKey, previousEndpoint := telemetry.WriteKey, telemetry.Endpoint
	telemetry.WriteKey, telemetry.Endpoint = key, endpoint
	t.Cleanup(func() { telemetry.WriteKey, telemetry.Endpoint = previousKey, previousEndpoint })
}

func aUserSpoolHolding(t *testing.T, commands ...string) telemetry.Spool {
	t.Helper()
	confighome.Isolate(t)
	spool, err := telemetry.OpenSpool()
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range commands {
		if err := spool.Append(aCompletedEvent(t, command)); err != nil {
			t.Fatal(err)
		}
	}
	return spool
}

func TestFlushSendsTheUserSpoolToTheBuildEndpointWithTheBuildKey(t *testing.T) {
	url, received := aCapturingServer(t, http.StatusOK)
	withBuildValues(t, "build-key", url)
	spool := aUserSpoolHolding(t, "deploy")

	telemetry.Flush(context.Background())

	batches := received()
	if len(batches) != 1 || batches[0].Body.APIKey != "build-key" || len(batches[0].Body.Batch) != 1 {
		t.Errorf("batches = %+v, want one batch of one event under the build key", batches)
	}
	if spool.HasEvents() {
		t.Error("the spool still holds the sent event")
	}
}

func TestFlushDoesNothingWithoutAnEndpointAKeyOrWhileTelemetryIsOffOrDebugging(t *testing.T) {
	cases := map[string]struct {
		key, endpointOf string
		setting         string
		doNotTrack      string
	}{
		"no endpoint":  {key: "k", endpointOf: "", setting: ""},
		"no key":       {key: "", endpointOf: "server", setting: ""},
		"opted out":    {key: "k", endpointOf: "server", setting: "0"},
		"do not track": {key: "k", endpointOf: "server", doNotTrack: "1"},
		"debug":        {key: "k", endpointOf: "server", setting: "debug"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			url, received := aCapturingServer(t, http.StatusOK)
			endpoint := ""
			if tc.endpointOf == "server" {
				endpoint = url
			}
			withBuildValues(t, tc.key, endpoint)
			t.Setenv("OCEL_TELEMETRY", tc.setting)
			t.Setenv("DO_NOT_TRACK", tc.doNotTrack)
			spool := aUserSpoolHolding(t, "deploy")

			telemetry.Flush(context.Background())

			if got := received(); len(got) != 0 {
				t.Errorf("server received %+v, want no request", got)
			}
			if !spool.HasEvents() {
				t.Error("the spool lost its events without a send")
			}
		})
	}
}
