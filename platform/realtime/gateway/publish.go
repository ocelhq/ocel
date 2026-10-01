package gateway

import (
	"encoding/json"
	"net/http"

	"github.com/ocelhq/ocel/platform/realtime/token"
)

const maxPublishBytes = MaxEventsPerPublish*MaxEventBytes*2 + 64<<10

type publishRequest struct {
	Channel string   `json:"channel"`
	Events  []string `json:"events"`
}

type publishAnswer struct {
	Successful []publishedEvent `json:"successful"`
	Failed     []failedEvent    `json:"failed"`
}

type publishedEvent struct {
	Identifier string `json:"identifier"`
	Index      int    `json:"index"`
}

type failedEvent struct {
	Identifier string `json:"identifier,omitempty"`
	Index      int    `json:"index"`
	Code       string `json:"code"`
	Message    string `json:"message"`
}

type Envelope struct {
	Version int             `json:"v"`
	ID      string          `json:"id"`
	Channel string          `json:"ch"`
	Time    int64           `json:"ts"`
	Kind    string          `json:"kind"`
	Data    json.RawMessage `json:"data"`
}

const (
	EnvelopeVersion = 1
	KindLive        = "live"
)

func (g *Gateway) servePublish(w http.ResponseWriter, r *http.Request) {
	var req publishRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPublishBytes)).Decode(&req); err != nil {
		answerError(w, http.StatusBadRequest, errorBadRequest, "a publish is a JSON object of a channel and its events")
		return
	}
	namespace, isChannel := namespaceOf(req.Channel, false)
	if !isChannel {
		answerError(w, http.StatusBadRequest, errorBadRequest, "channel "+req.Channel+" is no channel to publish on")
		return
	}
	if _, err := g.verify(r.Header.Get("Authorization"), namespace, token.Publish, req.Channel); err != nil {
		answerError(w, http.StatusUnauthorized, errorUnauthorized, err.Error())
		return
	}
	if len(req.Events) == 0 || len(req.Events) > MaxEventsPerPublish {
		answerError(w, http.StatusBadRequest, errorBadRequest, "a publish carries 1 to 5 events")
		return
	}
	answer := publishAnswer{Successful: []publishedEvent{}, Failed: []failedEvent{}}
	for i, event := range req.Events {
		if len(event) > MaxEventBytes {
			answer.Failed = append(answer.Failed, failedEvent{Index: i, Code: errorLimitExceeded, Message: "an event is at most 240 KB"})
			continue
		}
		var envelope Envelope
		if err := json.Unmarshal([]byte(event), &envelope); err != nil || !envelope.isLiveOn(req.Channel) {
			answer.Failed = append(answer.Failed, failedEvent{Index: i, Code: errorBadRequest, Message: "an event is a version 1 live envelope of v, id, ch, ts, kind and data, its ch the channel published on"})
			continue
		}
		g.hub.Publish(req.Channel, event)
		answer.Successful = append(answer.Successful, publishedEvent{Identifier: envelope.ID, Index: i})
	}
	writeAnswer(w, http.StatusOK, answer)
}

func (e Envelope) isLiveOn(channel string) bool {
	return e.Version == EnvelopeVersion && e.ID != "" && e.Channel == channel && e.Time > 0 && e.Kind == KindLive && len(e.Data) > 0
}

func answerError(w http.ResponseWriter, status int, errorType, message string) {
	writeAnswer(w, status, struct {
		Errors []errorMessage `json:"errors"`
	}{Errors: []errorMessage{{ErrorType: errorType, Message: message}}})
}

func writeAnswer(w http.ResponseWriter, status int, answer any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(mustEncode(answer))
}
