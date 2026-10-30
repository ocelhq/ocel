package endpoint

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	connect "connectrpc.com/connect"
)

func TestTheAuditLineQuotesWhatTheCallerAndThePathName(t *testing.T) {
	t.Parallel()

	var logged bytes.Buffer
	guard := &gate{
		verify: func(context.Context, string) (Caller, error) {
			return NewJobCaller("https://console.example.com", "org-1\nact=forged", Job{ID: "job\noutcome=pass"}), nil
		},
		organizationID: "org-1",
		endpointID:     "runner-1",
		errors:         connect.NewErrorWriter(),
		out:            &logged,
	}
	request := httptest.NewRequest(http.MethodPost, "/runner.v1.RunnerService/StartJob", nil)
	guard.wrap(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), request)

	if lines := strings.Count(strings.TrimSpace(logged.String()), "\n"); lines != 0 {
		t.Errorf("the audit log reads %q, want one line whatever the token names", logged.String())
	}
}
