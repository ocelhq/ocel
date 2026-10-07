package bastion

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/mmmorris1975/ssm-session-client/datachannel"
)

const receiveBufferBytes = 1 << 16

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
	return func(ctx context.Context, node, host string, port int) (Stream, error) {
		channel := new(datachannel.SsmDataChannel)
		err := channel.Open(cfg, &ssm.StartSessionInput{
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
