package envelope_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/envelope"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
)

const exactJSON = `{"ratio":2.0,"id":9007199254740993,"count":2}`

func answering(t *testing.T, status int, body string, received *atomic.Int32, seen *string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		if seen != nil {
			raw, _ := io.ReadAll(r.Body)
			*seen = string(raw)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func anEnvelope(payload string) *topicv1.Envelope {
	return &topicv1.Envelope{V: envelope.Version, Topic: "orders", Consumer: "email", Execution: "01K0000000000000000000000A-email", Payload: []byte(payload)}
}

func TestAnEnvelopeCarriesItsPayloadInlineAndByteForByte(t *testing.T) {
	var received atomic.Int32
	var seen string
	url := answering(t, http.StatusOK, exactJSON, &received, &seen)

	res := envelope.Post(context.Background(), context.Background(), http.DefaultClient, url, anEnvelope(exactJSON))

	if res.Outcome != envelope.Succeeded || string(res.Output) != exactJSON {
		t.Fatalf("Post = %+v, want success with the worker's output %s unchanged", res, exactJSON)
	}
	if !strings.Contains(seen, `"payload":`+exactJSON) {
		t.Errorf("the worker read %s, want the payload %s inline and unchanged", seen, exactJSON)
	}
}

type signing struct{ header, value string }

func (s signing) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set(s.header, s.value)
	return http.DefaultTransport.RoundTrip(req)
}

func TestAnEnvelopeIsPostedThroughTheClientTheEngineHandsIt(t *testing.T) {
	var signed atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signed.Store(r.Header.Get("X-Delivery-Secret"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	client := &http.Client{Transport: signing{header: "X-Delivery-Secret", value: "s3cret"}}

	res := envelope.Post(context.Background(), context.Background(), client, server.URL, anEnvelope(`{}`))

	if res.Outcome != envelope.Succeeded || signed.Load() != "s3cret" {
		t.Errorf("Post through a signing client = %+v with header %v, want success carrying the client's header", res, signed.Load())
	}
}

func TestAnEnvelopeDecodesBackWithEachPayloadAsTheJSONTextItWasSentAs(t *testing.T) {
	t.Parallel()

	for name, sent := range map[string]*topicv1.Envelope{
		"single": anEnvelope(exactJSON),
		"batch": {V: envelope.Version, Topic: "orders", Consumer: "email", Messages: []*topicv1.Delivery{
			{Execution: "a-email", Payload: []byte(exactJSON)},
			{Execution: "b-email", Payload: []byte(`[1,"two"]`)},
		}},
	} {
		encoded, err := envelope.Encode(sent)
		if err != nil {
			t.Fatalf("%s: Encode() = %v", name, err)
		}
		decoded, err := envelope.Decode(encoded)
		if err != nil {
			t.Fatalf("%s: Decode(%s) = %v", name, encoded, err)
		}
		if !proto.Equal(decoded, sent) {
			t.Errorf("%s: Decode(Encode()) = %v, want %v", name, decoded, sent)
		}
	}
}

func TestABodyThatIsNoJSONObjectDoesNotDecode(t *testing.T) {
	t.Parallel()

	for _, body := range []string{"", "[]", `{"topic":`} {
		if _, err := envelope.Decode([]byte(body)); err == nil {
			t.Errorf("Decode(%q) = nil error, want it refused", body)
		}
	}
}

func TestAnEnvelopeThatCannotBeEncodedIsRefusedWithoutAnAttemptAtTheWorker(t *testing.T) {
	var received atomic.Int32
	url := answering(t, http.StatusOK, "", &received, nil)

	res := envelope.Post(context.Background(), context.Background(), http.DefaultClient, url, anEnvelope(`{`))

	if res.Outcome != envelope.Refused || received.Load() != 0 {
		t.Errorf("Post = %+v with %d envelopes at the worker, want a refusal that reaches no worker", res, received.Load())
	}
}

func TestAWorkersAnswerIsReadAsSuccessAbortOrFailure(t *testing.T) {
	abort, err := protojson.Marshal(&topicv1.Answer{Outcome: &topicv1.Answer_Abort{Abort: &topicv1.Abort{Reason: "no such image"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   envelope.Outcome
		reason string
	}{
		{"an abort", http.StatusUnprocessableEntity, string(abort), envelope.Aborted, "no such image"},
		{"a failure", http.StatusInternalServerError, "boom", envelope.Failed, "boom"},
		{"an output over 256 KiB", http.StatusOK, `"` + strings.Repeat("a", 256<<10) + `"`, envelope.Refused, "256 KiB"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var received atomic.Int32
			res := envelope.Post(context.Background(), context.Background(), http.DefaultClient, answering(t, tc.status, tc.body, &received, nil), anEnvelope(`{}`))
			if res.Outcome != tc.want || !strings.Contains(res.Reason, tc.reason) {
				t.Errorf("Post = %+v, want outcome %v naming %q", res, tc.want, tc.reason)
			}
		})
	}
}

func TestAnAnswerReadWithoutHTTPIsJudgedAsAPostedOneIs(t *testing.T) {
	t.Parallel()

	abort, err := protojson.Marshal(&topicv1.Answer{Outcome: &topicv1.Answer_Abort{Abort: &topicv1.Abort{Reason: "no such image"}}})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", 600)
	for _, tc := range []struct {
		name   string
		status int
		text   string
		body   string
		want   envelope.Result
	}{
		{"a success with JSON", 200, "200 OK", exactJSON, envelope.Result{Outcome: envelope.Succeeded, Output: []byte(exactJSON)}},
		{"a success with no JSON", 204, "204 No Content", "", envelope.Result{Outcome: envelope.Succeeded}},
		{"an output over the cap", 200, "200 OK", `"` + strings.Repeat("a", 256<<10) + `"`, envelope.Result{Outcome: envelope.Refused, Reason: "the worker answered an output over the 256 KiB a run may store"}},
		{"an abort", 422, "422 Unprocessable Entity", string(abort), envelope.Result{Outcome: envelope.Aborted, Reason: "no such image"}},
		{"a failure", 500, "500 Internal Server Error", " boom \n", envelope.Result{Outcome: envelope.Failed, Reason: "the worker answered 500 Internal Server Error: boom"}},
		{"a long failure", 503, "503 Service Unavailable", long, envelope.Result{Outcome: envelope.Failed, Reason: "the worker answered 503 Service Unavailable: " + long[:512]}},
	} {
		got := envelope.AnswerOf(tc.status, tc.text, []byte(tc.body))
		if got.Outcome != tc.want.Outcome || got.Reason != tc.want.Reason || string(got.Output) != string(tc.want.Output) {
			t.Errorf("%s: AnswerOf(%d) = %+v, want %+v", tc.name, tc.status, got, tc.want)
		}
	}
}

func TestAnAttemptPastItsMaxDurationTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	attempt, cancel := context.WithTimeoutCause(context.Background(), 50*time.Millisecond, envelope.ErrTimedOut)
	defer cancel()

	if res := envelope.Post(context.Background(), attempt, http.DefaultClient, server.URL, anEnvelope(`{}`)); res.Outcome != envelope.TimedOut {
		t.Errorf("Post past its maxDuration = %+v, want it timed out", res)
	}
}
