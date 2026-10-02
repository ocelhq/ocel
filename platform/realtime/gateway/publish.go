package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/ocelhq/ocel/platform/realtime/token"
)

const serverSubject = "server"

var envelopeID = regexp.MustCompile(`^[0-9a-f]{32}$`)

type publishedEnvelope struct {
	Version   *int            `json:"v"`
	ID        string          `json:"id"`
	Channel   string          `json:"ch"`
	Timestamp *int64          `json:"ts"`
	Kind      string          `json:"kind"`
	Data      json.RawMessage `json:"data"`
}

func (e publishedEnvelope) isWellFormed() bool {
	return e.Version != nil && *e.Version == 1 &&
		envelopeID.MatchString(e.ID) &&
		e.Timestamp != nil &&
		e.Kind == "live" &&
		e.Data != nil
}

func (g *Gateway) servePublish(w http.ResponseWriter, r *http.Request) {
	credential, isBearer := readBearer(r.Header.Get("Authorization"))
	if !isBearer {
		answerError(w, http.StatusUnauthorized, errorUnauthorized, "a publish carries Authorization: Bearer <token>")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxEventBytes))
	if tooLarge := (*http.MaxBytesError)(nil); errors.As(err, &tooLarge) {
		answerError(w, http.StatusRequestEntityTooLarge, errorLimitExceeded, "an event is at most 240 KiB")
		return
	}
	var envelope publishedEnvelope
	if err != nil || json.Unmarshal(body, &envelope) != nil || !envelope.isWellFormed() {
		answerError(w, http.StatusBadRequest, errorBadRequest, `a publish is one JSON envelope {"v":1,"id","ch","ts","kind":"live","data"}`)
		return
	}
	namespace, isChannel := readPublishNamespace(envelope.Channel)
	if !isChannel {
		answerError(w, http.StatusBadRequest, errorBadRequest, "channel "+envelope.Channel+" is no channel to publish on")
		return
	}
	if _, err := g.verify(credential, token.Expected{Namespace: namespace, Operation: token.Publish, Channel: envelope.Channel, Subject: serverSubject}); err != nil {
		answerError(w, http.StatusUnauthorized, errorUnauthorized, err.Error())
		return
	}
	g.hub.Publish(envelope.Channel, string(body))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func readBearer(authorization string) (string, bool) {
	scheme, credential, _ := strings.Cut(authorization, " ")
	credential = strings.TrimSpace(credential)
	return credential, strings.EqualFold(scheme, "Bearer") && credential != ""
}

func answerError(w http.ResponseWriter, status int, kind errorType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(mustEncode(newErrorFrame("", "", kind, message)))
}
