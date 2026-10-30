package jobinput

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sync"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/encoding/protojson"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

const maxAnswerBytes = 64 << 10

type Holder interface {
	Hold(waiting *streamv1.WaitingEvent) (resume func(reason string))
}

type Answers struct {
	in     io.Reader
	start  sync.Once
	lines  chan []byte
	asking sync.Mutex
}

func New(in io.Reader) *Answers {
	return &Answers{in: in}
}

func (a *Answers) Confirm(ctx context.Context, holder Holder, finding, question string) (bool, error) {
	answer, err := a.ask(ctx, holder, &streamv1.WaitingEvent{Input: &runnerv1.JobInput{
		Need: &runnerv1.JobInput_Confirmation{Confirmation: &runnerv1.Confirmation{Finding: finding, Question: question}},
	}})
	if err != nil {
		return false, err
	}
	confirmed, ok := answer.GetAnswer().(*streamv1.InputAnswer_Confirmed)
	if !ok {
		return false, fmt.Errorf("input %s asks a question, and the console answered it as if it asked for values", answer.GetInputId())
	}
	return confirmed.Confirmed, nil
}

func (a *Answers) Resolve(ctx context.Context, holder Holder, needed *runnerv1.VariablesNeeded, missing *streamv1.MissingVariables) (bool, error) {
	answer, err := a.ask(ctx, holder, &streamv1.WaitingEvent{
		Missing: missing,
		Input:   &runnerv1.JobInput{Need: &runnerv1.JobInput_Variables{Variables: needed}},
	})
	if err != nil {
		return false, err
	}
	resolved, ok := answer.GetAnswer().(*streamv1.InputAnswer_Resolved)
	if !ok {
		return false, fmt.Errorf("input %s asks for values, and the console answered it as if it asked a question", answer.GetInputId())
	}
	return resolved.Resolved, nil
}

func (a *Answers) ask(ctx context.Context, holder Holder, waiting *streamv1.WaitingEvent) (*streamv1.InputAnswer, error) {
	a.asking.Lock()
	defer a.asking.Unlock()

	id, err := newInputID()
	if err != nil {
		return nil, err
	}
	waiting.Input.Id = id
	resume := holder.Hold(waiting)
	answer, err := a.await(ctx, id)
	if err != nil {
		resume("the input went unanswered")
		return nil, err
	}
	resume("answered")
	return answer, nil
}

func (a *Answers) await(ctx context.Context, id string) (*streamv1.InputAnswer, error) {
	a.start.Do(func() {
		a.lines = make(chan []byte)
		go a.read()
	})
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case line, open := <-a.lines:
			if !open {
				return nil, fmt.Errorf("stdin ended before the console answered input %s", id)
			}
			answer := &streamv1.InputAnswer{}
			if err := protojson.Unmarshal(line, answer); err != nil {
				return nil, fmt.Errorf("read the console's answer to input %s from stdin: %w", id, err)
			}
			if err := protovalidate.Validate(answer); err != nil {
				return nil, fmt.Errorf("read the console's answer to input %s from stdin: %w", id, err)
			}
			if answer.GetInputId() == id {
				return answer, nil
			}
		}
	}
}

func (a *Answers) read() {
	defer close(a.lines)
	scanner := bufio.NewScanner(a.in)
	scanner.Buffer(make([]byte, 0, 4096), maxAnswerBytes)
	for scanner.Scan() {
		if line := bytes.TrimSpace(scanner.Bytes()); len(line) > 0 {
			a.lines <- bytes.Clone(line)
		}
	}
}

func newInputID() (string, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("name this input: %w", err)
	}
	return "in_" + hex.EncodeToString(random), nil
}
