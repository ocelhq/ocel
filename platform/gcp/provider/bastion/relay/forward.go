package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/ocelhq/ocel/pkg/refusal"
)

const handshakeTimeout = 30 * time.Second

type Link struct {
	URL           string
	Target        string
	Token         func(ctx context.Context) (string, error)
	Warn          func(message string)
	ReportFailure func(error)
}

type Forward struct {
	address     string
	listener    net.Listener
	cancel      context.CancelFunc
	accepting   chan struct{}
	connections sync.WaitGroup
	once        sync.Once
	warned      sync.Once
	failed      sync.Once
}

type goneBastion struct{ refusal.Refusal }

func (g goneBastion) Unwrap() error { return g.Refusal }

func OpenForward(ctx context.Context, link Link) (*Forward, error) {
	probe, err := link.connect(ctx)
	if err != nil {
		return nil, err
	}
	_ = probe.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeNotReady, "listen on a loopback port to forward to %s from: %s", link.Target, err)
	}
	held, cancel := context.WithCancel(ctx)
	forward := &Forward{address: listener.Addr().String(), listener: listener, cancel: cancel, accepting: make(chan struct{})}
	go func() {
		defer close(forward.accepting)
		for {
			local, err := listener.Accept()
			if err != nil {
				return
			}
			forward.connections.Go(func() { forward.carry(held, link, local) })
		}
	}()
	go func() {
		<-held.Done()
		_ = listener.Close()
	}()
	return forward, nil
}

func (f *Forward) Address() string { return f.address }

func (f *Forward) Close() {
	f.once.Do(func() {
		f.cancel()
		_ = f.listener.Close()
		<-f.accepting
		f.connections.Wait()
	})
}

func (f *Forward) carry(ctx context.Context, link Link, local net.Conn) {
	defer local.Close()
	remote, err := link.connect(ctx)
	if err != nil {
		f.sayFailed(ctx, link, err)
		return
	}
	defer remote.Close()
	stop := context.AfterFunc(ctx, func() {
		_ = local.Close()
		_ = remote.Close()
	})
	defer stop()
	pipe(local, remote)
}

func (f *Forward) sayFailed(ctx context.Context, link Link, err error) {
	if ctx.Err() != nil {
		return
	}
	if gone := (goneBastion{}); errors.As(err, &gone) && link.ReportFailure != nil {
		f.failed.Do(func() { link.ReportFailure(err) })
		return
	}
	if link.Warn != nil {
		f.warned.Do(func() {
			link.Warn(fmt.Sprintf("A connection through the forward to %s failed, and the build saw it closed: %s", link.Target, err))
		})
	}
}

func (l Link) connect(ctx context.Context) (net.Conn, error) {
	token, err := l.Token(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	query := url.Values{TargetParam: {l.Target}}
	socket, resp, err := websocket.Dial(ctx, l.URL+Path+"?"+query.Encode(), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		return nil, l.refused(resp, err)
	}
	socket.SetReadLimit(messageLimit)
	return websocket.NetConn(context.WithoutCancel(ctx), socket, websocket.MessageBinary), nil
}

func (l Link) refused(resp *http.Response, dialed error) error {
	if resp == nil {
		return refusal.Refuse(refusal.CodeNotReady, "the bastion at %s could not be reached to forward to %s: %s", l.URL, l.Target, dialed)
	}
	switch resp.StatusCode {
	case http.StatusNotFound:
		return goneBastion{refusal.Refusal{Code: refusal.CodeNotReady,
			Message: fmt.Sprintf("the bastion at %s no longer exists (HTTP 404), so nothing forwards to %s", l.URL, l.Target)}}
	case http.StatusUnauthorized, http.StatusForbidden:
		return refusal.Refuse(refusal.CodeNotReady,
			"the bastion at %s refused the connection to %s (HTTP %d): it admits only an identity holding roles/run.invoker on it, and forwards to the tier's databases and caches alone",
			l.URL, l.Target, resp.StatusCode)
	}
	return refusal.Refuse(refusal.CodeNotReady, "the bastion at %s could not forward to %s (HTTP %d)", l.URL, l.Target, resp.StatusCode)
}
