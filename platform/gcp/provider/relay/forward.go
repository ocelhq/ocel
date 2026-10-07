package relay

import (
	"context"
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
	URL    string
	Target string
	Token  func(ctx context.Context) (string, error)
}

type Forward struct {
	address     string
	listener    net.Listener
	cancel      context.CancelFunc
	accepting   chan struct{}
	connections sync.WaitGroup
	once        sync.Once
}

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
		return nil, l.refused(resp)
	}
	socket.SetReadLimit(messageLimit)
	return websocket.NetConn(context.WithoutCancel(ctx), socket, websocket.MessageBinary), nil
}

func (l Link) refused(resp *http.Response) error {
	if resp == nil {
		return refusal.Refuse(refusal.CodeNotReady, "the bastion at %s could not be reached to forward to %s", l.URL, l.Target)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return refusal.Refuse(refusal.CodeNotReady,
			"the bastion at %s refused the connection to %s (HTTP %d): it admits only an identity holding roles/run.invoker on it, and forwards to the tier's databases and caches alone",
			l.URL, l.Target, resp.StatusCode)
	}
	return refusal.Refuse(refusal.CodeNotReady, "the bastion at %s could not forward to %s (HTTP %d)", l.URL, l.Target, resp.StatusCode)
}
