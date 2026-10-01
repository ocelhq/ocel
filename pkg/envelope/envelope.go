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
	Succeeded Outcome = iota
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

func encodeJSONVerbatim(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func Post(ctx, attemptCtx context.Context, url string, envelope *topicv1.Envelope) Result {
	body, err := Encode(envelope)
	if err != nil {
		return Result{Outcome: Refused, Reason: fmt.Sprintf("encode the envelope: %v", err)}
	}
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{Outcome: Failed, Reason: fmt.Sprintf("address the worker: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return classifyInterruption(ctx, attemptCtx, err)
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if err != nil {
		return classifyInterruption(ctx, attemptCtx, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if len(answer) > maxOutputBytes {
			return Result{Outcome: Refused, Reason: "the worker answered an output over the 256 KiB a run may store"}
		}
		if !json.Valid(answer) {
			answer = nil
		}
		return Result{Outcome: Succeeded, Output: answer}
	}
	var decoded topicv1.Answer
	if protojson.Unmarshal(answer, &decoded) == nil && decoded.GetAbort() != nil {
		return Result{Outcome: Aborted, Reason: decoded.GetAbort().GetReason()}
	}
	excerpt := answer
	if len(excerpt) > maxErrorExcerptBytes {
		excerpt = excerpt[:maxErrorExcerptBytes]
	}
	return Result{Outcome: Failed, Reason: fmt.Sprintf("the worker answered %s: %s", resp.Status, bytes.TrimSpace(excerpt))}
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
