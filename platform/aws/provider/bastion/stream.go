package bastion

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/ocelhq/ocel/pkg/tracing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	clientVersion        = "0.0.1"
	unacknowledgedLimit  = 1024
	resendInterval       = 200 * time.Millisecond
	unacknowledgedExpiry = 5 * time.Minute
	scope                = "github.com/ocelhq/ocel/platform/aws/provider/bastion"
)

type Stream interface {
	Receive() ([]byte, error)
	Send(payload []byte) error
	Terminate() error
}

type OpenSessionFunc func(ctx context.Context, node, host string, port int) (Stream, error)

type sessionStream struct {
	ws        *websocket.Conn
	writing   sync.Mutex
	window    chan struct{}
	done      chan struct{}
	terminate sync.Once

	expectedSequence int64
	early            map[int64]agentMessage

	mu             sync.Mutex
	sequence       int64
	unacknowledged []sentMessage
	expired        error
}

type sentMessage struct {
	msg         agentMessage
	firstSentAt time.Time
	lastSentAt  time.Time
}

func OpenSession(cfg aws.Config) OpenSessionFunc {
	return func(ctx context.Context, node, host string, port int) (_ Stream, err error) {
		ctx, span := otel.Tracer(scope).Start(ctx, "open bastion session",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("aws.ssm.target", node),
				attribute.String("server.address", host),
				attribute.Int("server.port", port),
			))
		defer func() { tracing.EndSpan(span, err) }()
		session, err := ssm.NewFromConfig(cfg).StartSession(ctx, &ssm.StartSessionInput{
			DocumentName: aws.String(PortForwardingDocument),
			Target:       aws.String(node),
			Parameters: map[string][]string{
				"host":            {host},
				"portNumber":      {strconv.Itoa(port)},
				"localPortNumber": {"0"},
			},
		})
		if err != nil {
			return nil, fmt.Errorf("start a %s session on %s: %w", PortForwardingDocument, node, err)
		}
		ws, _, err := websocket.DefaultDialer.DialContext(ctx, aws.ToString(session.StreamUrl), nil)
		if err != nil {
			return nil, fmt.Errorf("connect to the session on %s: %w", node, err)
		}
		stream := &sessionStream{
			ws:     ws,
			window: make(chan struct{}, unacknowledgedLimit),
			done:   make(chan struct{}),
			early:  map[int64]agentMessage{},
		}
		stopWatching := context.AfterFunc(ctx, func() { _ = ws.Close() })
		defer stopWatching()
		if err := stream.handshake(aws.ToString(session.TokenValue)); err != nil {
			stream.stop()
			return nil, fmt.Errorf("handshake the session on %s: %w", node, errors.Join(ctx.Err(), err))
		}
		go stream.resendUnacknowledged()
		return stream, nil
	}
}

func (s *sessionStream) handshake(token string) error {
	s.writing.Lock()
	err := s.ws.WriteJSON(map[string]string{
		"MessageSchemaVersion": "1.0",
		"RequestId":            uuid.NewString(),
		"TokenValue":           token,
	})
	s.writing.Unlock()
	if err != nil {
		return err
	}
	for {
		msg, err := s.next()
		if err != nil {
			return err
		}
		switch msg.payloadType {
		case payloadHandshakeRequest:
			response, err := handshakeResponseTo(msg.payload)
			if err != nil {
				return err
			}
			if err := s.send(payloadHandshakeResponse, response); err != nil {
				return err
			}
		case payloadHandshakeComplete:
			return nil
		}
	}
}

func handshakeResponseTo(payload []byte) ([]byte, error) {
	var request struct {
		RequestedClientActions []struct{ ActionType string }
	}
	if err := json.Unmarshal(payload, &request); err != nil {
		return nil, fmt.Errorf("read the agent's handshake request: %w", err)
	}
	type processedAction struct {
		ActionType   string
		ActionStatus int
		Error        string `json:",omitempty"`
	}
	const succeeded, unsupported = 1, 3
	processed := make([]processedAction, 0, len(request.RequestedClientActions))
	for _, action := range request.RequestedClientActions {
		if action.ActionType == "SessionType" {
			processed = append(processed, processedAction{ActionType: action.ActionType, ActionStatus: succeeded})
			continue
		}
		processed = append(processed, processedAction{ActionType: action.ActionType, ActionStatus: unsupported, Error: action.ActionType + " is unsupported"})
	}
	return json.Marshal(struct {
		ClientVersion          string
		ProcessedClientActions []processedAction
	}{clientVersion, processed})
}

func (s *sessionStream) Receive() ([]byte, error) {
	for {
		msg, err := s.next()
		if err != nil {
			return nil, err
		}
		if msg.payloadType == payloadOutput {
			return msg.payload, nil
		}
	}
}

func (s *sessionStream) Send(payload []byte) error {
	return s.send(payloadOutput, slices.Clone(payload))
}

func (s *sessionStream) Terminate() error {
	var err error
	s.terminate.Do(func() {
		close(s.done)
		s.mu.Lock()
		msg := agentMessage{
			messageType:    inputStreamData,
			sequenceNumber: s.nextSequence(),
			flags:          flagData,
			id:             uuid.New(),
			payloadType:    payloadFlag,
			payload:        binary.BigEndian.AppendUint32(nil, terminateSessionFlag),
		}
		s.mu.Unlock()
		_ = s.write(msg)
		err = s.ws.Close()
	})
	return err
}

func (s *sessionStream) stop() {
	s.terminate.Do(func() {
		close(s.done)
		_ = s.ws.Close()
	})
}

func (s *sessionStream) next() (agentMessage, error) {
	for {
		if msg, ok := s.early[s.expectedSequence]; ok {
			delete(s.early, s.expectedSequence)
			s.expectedSequence++
			return msg, nil
		}
		_, data, err := s.ws.ReadMessage()
		if err != nil {
			return agentMessage{}, s.expiredOr(err)
		}
		msg, err := parseAgentMessage(data)
		if err != nil {
			return agentMessage{}, err
		}
		switch msg.messageType {
		case acknowledgeMessage:
			if err := s.acknowledged(msg.payload); err != nil {
				return agentMessage{}, err
			}
		case channelClosedMessage:
			return agentMessage{}, channelClosedError(msg.payload)
		case outputStreamData:
			if err := s.acknowledge(msg); err != nil {
				return agentMessage{}, err
			}
			if msg.sequenceNumber == s.expectedSequence {
				s.expectedSequence++
				return msg, nil
			}
			if msg.sequenceNumber > s.expectedSequence {
				s.early[msg.sequenceNumber] = msg
			}
		}
	}
}

func channelClosedError(payload []byte) error {
	var closed struct{ Output string }
	if json.Unmarshal(payload, &closed) == nil && closed.Output != "" {
		return fmt.Errorf("the agent closed the session: %s: %w", closed.Output, io.EOF)
	}
	return io.EOF
}

func (s *sessionStream) send(payloadType uint32, payload []byte) error {
	select {
	case s.window <- struct{}{}:
	case <-s.done:
		return net.ErrClosed
	}
	now := time.Now()
	s.mu.Lock()
	msg := agentMessage{
		messageType:    inputStreamData,
		sequenceNumber: s.nextSequence(),
		flags:          flagData,
		id:             uuid.New(),
		payloadType:    payloadType,
		payload:        payload,
	}
	s.unacknowledged = append(s.unacknowledged, sentMessage{msg: msg, firstSentAt: now, lastSentAt: now})
	s.mu.Unlock()
	return s.write(msg)
}

func (s *sessionStream) acknowledge(received agentMessage) error {
	payload, err := json.Marshal(map[string]any{
		"AcknowledgedMessageType":           received.messageType,
		"AcknowledgedMessageId":             received.id.String(),
		"AcknowledgedMessageSequenceNumber": received.sequenceNumber,
		"IsSequentialMessage":               true,
	})
	if err != nil {
		return err
	}
	return s.write(agentMessage{
		messageType:    acknowledgeMessage,
		sequenceNumber: received.sequenceNumber,
		flags:          flagAck,
		id:             uuid.New(),
		payloadType:    payloadUndefined,
		payload:        payload,
	})
}

func (s *sessionStream) acknowledged(payload []byte) error {
	var acknowledgement struct {
		AcknowledgedMessageSequenceNumber int64
	}
	if err := json.Unmarshal(payload, &acknowledgement); err != nil {
		return fmt.Errorf("read the agent's acknowledgement: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.unacknowledged, func(sent sentMessage) bool {
		return sent.msg.sequenceNumber == acknowledgement.AcknowledgedMessageSequenceNumber
	})
	if i < 0 {
		return nil
	}
	s.unacknowledged = slices.Delete(s.unacknowledged, i, i+1)
	<-s.window
	return nil
}

func (s *sessionStream) resendUnacknowledged() {
	ticker := time.NewTicker(resendInterval / 2)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
		}
		s.mu.Lock()
		if len(s.unacknowledged) == 0 {
			s.mu.Unlock()
			continue
		}
		oldest := &s.unacknowledged[0]
		now := time.Now()
		if now.Sub(oldest.firstSentAt) > unacknowledgedExpiry {
			s.expired = fmt.Errorf("the bastion's agent acknowledged none of the %d bytes sent %s ago", len(oldest.msg.payload), unacknowledgedExpiry)
			s.mu.Unlock()
			_ = s.ws.Close()
			return
		}
		if now.Sub(oldest.lastSentAt) < resendInterval {
			s.mu.Unlock()
			continue
		}
		oldest.lastSentAt = now
		msg := oldest.msg
		s.mu.Unlock()
		_ = s.write(msg)
	}
}

func (s *sessionStream) nextSequence() int64 {
	sequence := s.sequence
	s.sequence++
	return sequence
}

func (s *sessionStream) write(msg agentMessage) error {
	s.writing.Lock()
	defer s.writing.Unlock()
	return s.ws.WriteMessage(websocket.BinaryMessage, msg.MarshalBinary())
}

func (s *sessionStream) expiredOr(err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expired != nil {
		return errors.Join(s.expired, err)
	}
	return err
}
