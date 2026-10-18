package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/ocelhq/ocel/platform/realtime/token"
)

const (
	authorizationPrefix            = "header-"
	maxClientFrameBytes            = 64 << 10
	writeTimeout                   = 10 * time.Second
	keepAlivesPerConnectionTimeout = 5
)

var subscriptionID = regexp.MustCompile(`^[a-zA-Z0-9_+-]{1,128}$`)

type connection struct {
	g         *Gateway
	conn      *websocket.Conn
	namespace string
	expiresAt time.Time
	queue     *queue

	subscriptions map[string]func()
}

func (g *Gateway) serveSocket(w http.ResponseWriter, r *http.Request) {
	if err := g.trackSocket(); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer g.releaseSocket()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:       []string{Subprotocol},
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	if conn.Subprotocol() != Subprotocol {
		_ = conn.Close(websocket.StatusProtocolError, "speak the "+Subprotocol+" subprotocol")
		return
	}
	conn.SetReadLimit(maxClientFrameBytes)

	connectToken, err := readConnectToken(r)
	if err != nil {
		g.refuseConnection(conn, errorUnauthorized, err)
		return
	}
	claims, err := g.verify(connectToken, token.Expected{Operation: token.Connect})
	if err != nil {
		g.refuseConnection(conn, errorUnauthorized, err)
		return
	}
	if err := g.trackToken(connectToken); err != nil {
		g.refuseConnection(conn, errorLimitExceeded, err)
		return
	}
	defer g.releaseToken(connectToken)
	c := &connection{
		g:             g,
		conn:          conn,
		namespace:     claims.Ocel.Namespace,
		expiresAt:     time.Unix(claims.ExpiresAt, 0),
		queue:         newQueue(g.cfg.QueueBudgetBytes),
		subscriptions: map[string]func(){},
	}
	c.serve()
}

func readConnectToken(r *http.Request) (string, error) {
	var auth authorization
	for _, offered := range offeredSubprotocols(r) {
		encoded, isAuthorization := strings.CutPrefix(offered, authorizationPrefix)
		if !isAuthorization {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil || json.Unmarshal(raw, &auth) != nil {
			return "", errors.New("the authorization subprotocol is no base64url JSON object")
		}
	}
	if auth.Token == "" {
		return "", errors.New("the connection carries no Authorization in its " + authorizationPrefix + " subprotocol")
	}
	return auth.Token, nil
}

func offeredSubprotocols(r *http.Request) []string {
	var offered []string
	for _, header := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, protocol := range strings.Split(header, ",") {
			offered = append(offered, strings.TrimSpace(protocol))
		}
	}
	return offered
}

func (g *Gateway) verify(raw string, want token.Expected) (token.Claims, error) {
	want.Audience = g.cfg.Host
	return token.Verify(raw, g.cfg.Keys, g.cfg.Clock.Now(), want)
}

func (g *Gateway) refuseConnection(conn *websocket.Conn, kind errorType, reason error) {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	_ = writeJSON(ctx, conn, newErrorFrame(frameConnectionError, "", kind, reason.Error()))
	_ = conn.Close(websocket.StatusPolicyViolation, string(kind))
}

func (c *connection) serve() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopWatchingShutdown := context.AfterFunc(c.g.shutdown, func() {
		_ = c.conn.Close(websocket.StatusGoingAway, "the realtime gateway is shutting down")
	})
	defer stopWatchingShutdown()
	expired := make(chan struct{})
	stopWatchingExpiry := c.g.cfg.Clock.AfterFunc(c.expiresAt.Sub(c.g.cfg.Clock.Now()), func() { close(expired) })
	defer stopWatchingExpiry()
	var running sync.WaitGroup
	running.Go(func() {
		defer cancel()
		c.write(ctx, expired)
	})
	running.Go(func() { c.ping(ctx) })
	c.read(ctx)
	cancel()
	running.Wait()
	for _, unsubscribe := range c.subscriptions {
		unsubscribe()
	}
	_ = c.conn.CloseNow()
}

func (c *connection) ping(ctx context.Context) {
	for c.wait(ctx, c.g.cfg.PingInterval) {
		pong, cancel := context.WithCancel(ctx)
		stopWaitingForPong := c.g.cfg.Clock.AfterFunc(c.g.cfg.PongTimeout, cancel)
		err := c.conn.Ping(pong)
		stopWaitingForPong()
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				_ = c.conn.CloseNow()
			}
			return
		}
	}
}

func (c *connection) wait(ctx context.Context, d time.Duration) bool {
	elapsed := make(chan struct{})
	stop := c.g.cfg.Clock.AfterFunc(d, func() { close(elapsed) })
	defer stop()
	select {
	case <-elapsed:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *connection) write(ctx context.Context, expired <-chan struct{}) {
	keepAlive := time.NewTicker(c.g.cfg.KeepAlive)
	defer keepAlive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-expired:
			_ = c.conn.Close(websocket.StatusPolicyViolation, "the connection's token has expired")
			return
		case <-c.queue.Overflowed():
			_ = c.conn.Close(websocket.StatusTryAgainLater, "the connection fell behind its events")
			return
		case <-keepAlive.C:
			if c.send(ctx, mustEncode(idFrame{Type: frameKeepAlive})) != nil {
				return
			}
		case <-c.queue.Ready():
			for _, frame := range c.queue.Take() {
				if c.send(ctx, frame) != nil {
					return
				}
			}
		}
	}
}

func (c *connection) send(ctx context.Context, frame []byte) error {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return c.conn.Write(ctx, websocket.MessageText, frame)
}

func (c *connection) read(ctx context.Context) {
	for {
		_, raw, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
		var frame clientFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			c.replyError(frameError, "", errorBadRequest, "a frame is a JSON object with a type")
			continue
		}
		switch frame.Type {
		case frameConnectionInit:
			c.reply(ackFrame{Type: frameConnectionAck, ConnectionTimeoutMs: (keepAlivesPerConnectionTimeout * c.g.cfg.KeepAlive).Milliseconds()})
		case frameSubscribe:
			c.subscribe(frame)
		case frameUnsubscribe:
			c.unsubscribe(frame)
		case framePublish:
			c.replyError(framePublishError, frame.ID, errorUnsupportedOperation, "a browser publishes through the app's realtime handler, which publishes from the server")
		default:
			c.replyError(frameError, frame.ID, errorUnknownOperation, "unknown frame type "+string(frame.Type))
		}
	}
}

func (c *connection) subscribe(frame clientFrame) {
	namespace, isChannel := readSubscribeNamespace(frame.Channel)
	switch {
	case !subscriptionID.MatchString(frame.ID):
		c.replyError(frameSubscribeError, frame.ID, errorBadRequest, "a subscription id is 1 to 128 letters, digits, _, + or -")
		return
	case c.subscriptions[frame.ID] != nil:
		c.replyError(frameSubscribeError, frame.ID, errorBadRequest, "subscription id "+frame.ID+" is already in use on this connection")
		return
	case len(c.subscriptions) >= maxSubscriptions:
		c.replyError(frameSubscribeError, frame.ID, errorLimitExceeded, "a connection holds at most 200 subscriptions")
		return
	case !isChannel || namespace != c.namespace:
		c.replyError(frameSubscribeError, frame.ID, errorBadRequest, "channel "+frame.Channel+" is not a channel of /"+c.namespace)
		return
	}
	if _, err := c.g.verify(frame.Authorization.Token, token.Expected{Namespace: namespace, Operation: token.Subscribe, Channel: frame.Channel}); err != nil {
		c.replyError(frameSubscribeError, frame.ID, errorUnauthorized, err.Error())
		return
	}
	c.subscriptions[frame.ID] = c.g.hub.Subscribe(frame.Channel, frame.ID, c.queue)
	c.reply(idFrame{Type: frameSubscribeSuccess, ID: frame.ID})
}

func (c *connection) unsubscribe(frame clientFrame) {
	unsubscribe, subscribed := c.subscriptions[frame.ID]
	if !subscribed {
		c.replyError(frameUnsubscribeError, frame.ID, errorUnknownOperation, "Unknown operation id "+frame.ID)
		return
	}
	unsubscribe()
	delete(c.subscriptions, frame.ID)
	c.reply(idFrame{Type: frameUnsubscribeSuccess, ID: frame.ID})
}

func (c *connection) reply(frame any) {
	c.queue.Offer(mustEncode(frame))
}

func (c *connection) replyError(of frameType, id string, kind errorType, message string) {
	c.reply(newErrorFrame(of, id, kind, message))
}

func writeJSON(ctx context.Context, conn *websocket.Conn, frame any) error {
	return conn.Write(ctx, websocket.MessageText, mustEncode(frame))
}

func mustEncode(frame any) []byte {
	raw, err := json.Marshal(frame)
	if err != nil {
		panic(err)
	}
	return raw
}
