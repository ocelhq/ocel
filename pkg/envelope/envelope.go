package envelope

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
)

const (
	Version = 1

	maxAnswerBytes       = 1 << 20
	maxOutputBytes       = 256 << 10
	maxErrorExcerptBytes = 512
)

var (
	ErrTimedOut = errors.New("the attempt ran past its maxDuration")
	ErrCanceled = errors.New("the run was canceled")

	schemaDigest = regexp.MustCompile(`^sha256-[0-9a-f]{64}$`)
)

type Outcome int

const (
	Unknown Outcome = iota
	Succeeded
	Aborted
	TimedOut
	Failed
	Canceled
	Interrupted
	Refused
)

type Result struct {
	Outcome Outcome
	Output  json.RawMessage
	Reason  string
}

func SchemaOf(schema string) string {
	if schema == "" || schemaDigest.MatchString(schema) {
		return schema
	}
	sum := sha256.Sum256([]byte(schema))
	return "sha256-" + hex.EncodeToString(sum[:])
}

func Encode(envelope *topicv1.Envelope) ([]byte, error) {
	bare := proto.CloneOf(envelope)
	bare.Payload = nil
	for _, delivery := range bare.GetMessages() {
		delivery.Payload = nil
	}
	encoded, err := protojson.Marshal(bare)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, err
	}
	if len(envelope.GetPayload()) > 0 {
		fields["payload"] = envelope.GetPayload()
	}
	if len(envelope.GetMessages()) > 0 {
		var deliveries []map[string]json.RawMessage
		if err := json.Unmarshal(fields["messages"], &deliveries); err != nil {
			return nil, err
		}
		for i, delivery := range envelope.GetMessages() {
			if len(delivery.GetPayload()) > 0 {
				deliveries[i]["payload"] = delivery.GetPayload()
			}
		}
		if fields["messages"], err = encodeJSONVerbatim(deliveries); err != nil {
			return nil, err
		}
	}
	return encodeJSONVerbatim(fields)
}

func Decode(body []byte) (*topicv1.Envelope, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	payload := fields["payload"]
	delete(fields, "payload")
	var deliveries []map[string]json.RawMessage
	if raw, found := fields["messages"]; found {
		if err := json.Unmarshal(raw, &deliveries); err != nil {
			return nil, err
		}
	}
	payloads := make([]json.RawMessage, len(deliveries))
	for i, delivery := range deliveries {
		payloads[i] = delivery["payload"]
		delete(delivery, "payload")
	}
	if deliveries != nil {
		raw, err := json.Marshal(deliveries)
		if err != nil {
			return nil, err
		}
		fields["messages"] = raw
	}
	bare, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	decoded := &topicv1.Envelope{}
	if err := protojson.Unmarshal(bare, decoded); err != nil {
		return nil, err
	}
	decoded.Payload = payload
	for i, delivery := range decoded.GetMessages() {
		delivery.Payload = payloads[i]
	}
	return decoded, nil
}

func encodeJSONVerbatim(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func Post(ctx, attemptCtx context.Context, client *http.Client, url string, envelope *topicv1.Envelope) Result {
	body, err := Encode(envelope)
	if err != nil {
		return Result{Outcome: Refused, Reason: fmt.Sprintf("encode the envelope: %v", err)}
	}
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{Outcome: Failed, Reason: fmt.Sprintf("address the worker: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return classifyInterruption(ctx, attemptCtx, err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil {
		return classifyInterruption(ctx, attemptCtx, err)
	}
	return AnswerOf(resp.StatusCode, resp.Status, answer)
}

func AnswerOf(status int, statusText string, body []byte) Result {
	if status >= 200 && status < 300 {
		if len(body) > maxOutputBytes {
			return Result{Outcome: Refused, Reason: fmt.Sprintf("the worker answered an output over the %d KiB a run may store", maxOutputBytes>>10)}
		}
		if !json.Valid(body) {
			body = nil
		}
		return Result{Outcome: Succeeded, Output: body}
	}
	var decoded topicv1.Answer
	if protojson.Unmarshal(body, &decoded) == nil && decoded.GetAbort() != nil {
		return Result{Outcome: Aborted, Reason: decoded.GetAbort().GetReason()}
	}
	excerpt := body
	if len(excerpt) > maxErrorExcerptBytes {
		excerpt = excerpt[:maxErrorExcerptBytes]
	}
	return Result{Outcome: Failed, Reason: fmt.Sprintf("the worker answered %s: %s", statusText, bytes.TrimSpace(excerpt))}
}

func classifyInterruption(ctx, attemptCtx context.Context, err error) Result {
	switch cause := context.Cause(attemptCtx); {
	case errors.Is(cause, ErrTimedOut):
		return Result{Outcome: TimedOut, Reason: cause.Error()}
	case errors.Is(cause, ErrCanceled):
		return Result{Outcome: Canceled}
	case ctx.Err() != nil:
		return Result{Outcome: Interrupted}
	}
	return Result{Outcome: Failed, Reason: fmt.Sprintf("reach the worker: %v", err)}
}
