package console

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

func TestSigningInKeepsTheDeviceCodeAndTokenTheConsoleHandsOverOutOfTheTrace(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		if strings.HasSuffix(r.URL.Path, "/device/code") {
			_, _ = w.Write([]byte(`{"error":"invalid_request","device_code":"dc-5e1f9","user_code":"uc-77b2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"error":"authorization_pending","access_token":"at-91c4"}`))
	}))
	t.Cleanup(server.Close)
	client := New(server.URL)

	_, _ = client.RequestDeviceCode(ctx)
	_, _ = client.PollToken(ctx, "dc-5e1f9")

	if len(recorded.Ended()) == 0 {
		t.Fatal("no span was recorded, want the sign-in requests traced")
	}
	for _, span := range recorded.Ended() {
		exported := tracingtest.ExportedText(span)
		for _, secret := range []string{"dc-5e1f9", "uc-77b2", "at-91c4"} {
			if strings.Contains(exported, secret) {
				t.Errorf("trace carries %q:\n%s", secret, exported)
			}
		}
	}
}
