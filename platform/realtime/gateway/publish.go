package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ocelhq/ocel/platform/realtime/token"
)

type publishedEnvelope struct {
	Channel string `json:"ch"`
}

func (g *Gateway) servePublish(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxEventBytes))
	if tooLarge := (*http.MaxBytesError)(nil); errors.As(err, &tooLarge) {
		answerError(w, http.StatusRequestEntityTooLarge, errorLimitExceeded, "an event is at most 240 KB")
		return
	}
	var envelope publishedEnvelope
	if err != nil || json.Unmarshal(body, &envelope) != nil {
		answerError(w, http.StatusBadRequest, errorBadRequest, "a publish is one JSON envelope naming its channel in ch")
		return
	}
	namespace, isChannel := readPublishNamespace(envelope.Channel)
	if !isChannel {
		answerError(w, http.StatusBadRequest, errorBadRequest, "channel "+envelope.Channel+" is no channel to publish on")
		return
	}
	credential, isBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !isBearer {
		answerError(w, http.StatusUnauthorized, errorUnauthorized, "a publish carries Authorization: Bearer <token>")
		return
	}
	if _, err := g.verify(credential, namespace, token.Publish, envelope.Channel); err != nil {
		answerError(w, http.StatusUnauthorized, errorUnauthorized, err.Error())
		return
	}
	g.hub.Publish(envelope.Channel, string(body))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func answerError(w http.ResponseWriter, status int, kind errorType, message string) {
	writeAnswer(w, status, newErrorFrame("", "", kind, message))
}

func writeAnswer(w http.ResponseWriter, status int, answer any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(mustEncode(answer))
}
