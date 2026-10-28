package bastion

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/smithy-go/middleware"
	"github.com/mmmorris1975/ssm-session-client/datachannel"
	"github.com/ocelhq/ocel/pkg/tracing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	receiveBufferBytes = 1 << 16
	scope              = "github.com/ocelhq/ocel/platform/aws/provider/bastion"
)

type Stream interface {
	Receive() ([]byte, error)
	Send(payload []byte) error
	Terminate() error
}

type OpenSessionFunc func(ctx context.Context, node, host string, port int) (Stream, error)

type sessionStream struct {
	channel   *datachannel.SsmDataChannel
	buf       []byte
	terminate sync.Once
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
		channel := new(datachannel.SsmDataChannel)
		err = channel.Open(parentCallsUnder(cfg, span), &ssm.StartSessionInput{
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
		stopWatching := context.AfterFunc(ctx, func() { _ = channel.Close() })
		defer stopWatching()
		if err := channel.WaitForHandshakeComplete(ctx); err != nil {
			_ = channel.Close()
			return nil, fmt.Errorf("handshake the session on %s: %w", node, err)
		}
		return &sessionStream{channel: channel, buf: make([]byte, receiveBufferBytes)}, nil
	}
}

func parentCallsUnder(cfg aws.Config, span trace.Span) aws.Config {
	cfg.APIOptions = append(slices.Clone(cfg.APIOptions), func(stack *middleware.Stack) error {
		return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("OcelSessionSpan",
			func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
				return next.HandleInitialize(trace.ContextWithSpan(ctx, span), in)
			}), middleware.Before)
	})
	return cfg
}

func (s *sessionStream) Receive() ([]byte, error) {
	for {
		n, err := s.channel.Read(s.buf)
		if err != nil {
			return nil, err
		}
		payload, err := s.channel.HandleMsg(s.buf[:n])
		if len(payload) > 0 || err != nil {
			return payload, err
		}
	}
}

func (s *sessionStream) Send(payload []byte) error {
	_, err := s.channel.Write(payload)
	return err
}

func (s *sessionStream) Terminate() error {
	var err error
	s.terminate.Do(func() {
		_ = s.channel.TerminateSession()
		err = s.channel.Close()
	})
	return err
}
