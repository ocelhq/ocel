package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/ocelhq/ocel/platform/realtime/token"
)

const (
	authorizationPrefix = "header-"
	maxClientFrameBytes = 64 << 10
	writeTimeout        = 10 * time.Second
	connectionTimeouts  = 5
)

var subscriptionID = regexp.MustCompile(`^[a-zA-Z0-9_+-]{1,128}$`)

type connection struct {
	g         *Gateway
	conn      *websocket.Conn
	namespace string
	queue     *Queue

	subscriptions map[string]func()
}

func (g *Gateway) serveSocket(w http.ResponseWriter, r *http.Request) {
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

	namespace, err := g.verifyConnect(r)
	if err != nil {
		g.refuseConnection(conn, err)
		return
	}
	c := &connection{
		g:             g,
		conn:          conn,
		namespace:     namespace,
		queue:         NewQueue(g.cfg.QueueBudget),
		subscriptions: map[string]func(){},
	}
	c.serve()
}

func (g *Gateway) verifyConnect(r *http.Request) (string, error) {
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
	namespace, err := token.ReadUnverifiedNamespace(auth.Token)
	if err != nil {
		return "", err
	}
	if _, err := g.verify(auth.Token, namespace, token.Connect, "/"+namespace); err != nil {
		return "", err
	}
	return namespace, nil
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

func (g *Gateway) verify(raw, namespace string, op token.Operation, channel string) (token.Claims, error) {
	key, known := g.cfg.Keys(namespace)
	if !known {
		return token.Claims{}, &token.Refusal{Reason: token.ReasonNamespace}
	}
	return token.Verify(raw, key, g.cfg.Now(), token.Expected{
		Audience:  g.cfg.Host,
		Namespace: namespace,
		Operation: op,
		Channel:   channel,
	})
}

func (g *Gateway) refuseConnection(conn *websocket.Conn, reason error) {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	_ = writeJSON(ctx, conn, errorFrame{Type: frameConnectionError, Errors: []errorMessage{{ErrorType: errorUnauthorized, Message: reason.Error()}}})
	_ = conn.Close(websocket.StatusPolicyViolation, "unauthorized")
}

func (c *connection) serve() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	written := make(chan struct{})
	go func() {
		defer close(written)
		defer cancel()
		c.write(ctx)
	}()
	c.read(ctx)
	cancel()
	<-written
	for _, unsubscribe := range c.subscriptions {
		unsubscribe()
	}
	_ = c.conn.CloseNow()
}

func (c *connection) write(ctx context.Context) {
	keepAlive := time.NewTicker(c.g.cfg.KeepAlive)
	defer keepAlive.Stop()
	for {
		select {
		case <-ctx.Done():
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
			c.reply(errorFrame{Type: frameError, Errors: []errorMessage{{ErrorType: errorBadRequest, Message: "a frame is a JSON object with a type"}}})
			continue
		}
		switch frame.Type {
		case frameConnectionInit:
			c.reply(ackFrame{Type: frameConnectionAck, ConnectionTimeoutMs: (connectionTimeouts * c.g.cfg.KeepAlive).Milliseconds()})
		case frameSubscribe:
			c.subscribe(frame)
		case frameUnsubscribe:
			c.unsubscribe(frame)
		case framePublish:
			c.reply(errorFrame{Type: framePublishError, ID: frame.ID, Errors: []errorMessage{{ErrorType: errorUnsupportedOp, Message: "a browser publishes through the app's realtime handler, which publishes from the server"}}})
		default:
			c.reply(errorFrame{Type: frameError, ID: frame.ID, Errors: []errorMessage{{ErrorType: errorUnknownOp, Message: "unknown frame type " + frame.Type}}})
		}
	}
}

func (c *connection) subscribe(frame clientFrame) {
	refuse := func(errorType, message string) {
		c.reply(errorFrame{Type: frameSubscribeError, ID: frame.ID, Errors: []errorMessage{{ErrorType: errorType, Message: message}}})
	}
	namespace, isChannel := namespaceOf(frame.Channel, true)
	switch {
	case !subscriptionID.MatchString(frame.ID):
		refuse(errorBadRequest, "a subscription id is 1 to 128 letters, digits, _, + or -")
		return
	case c.subscriptions[frame.ID] != nil:
		refuse(errorBadRequest, "subscription id "+frame.ID+" is already in use on this connection")
		return
	case len(c.subscriptions) >= MaxSubscriptions:
		refuse(errorLimitExceeded, "a connection holds at most 200 subscriptions")
		return
	case !isChannel || namespace != c.namespace:
		refuse(errorBadRequest, "channel "+frame.Channel+" is not a channel of /"+c.namespace)
		return
	}
	if _, err := c.g.verify(frame.Authorization.Token, namespace, token.Subscribe, frame.Channel); err != nil {
		refuse(errorUnauthorized, err.Error())
		return
	}
	c.reply(idFrame{Type: frameSubscribeSuccess, ID: frame.ID})
	c.subscriptions[frame.ID] = c.g.hub.Subscribe(frame.Channel, frame.ID, c.queue)
}

func (c *connection) unsubscribe(frame clientFrame) {
	unsubscribe, subscribed := c.subscriptions[frame.ID]
	if !subscribed {
		c.reply(errorFrame{Type: frameUnsubscribeError, ID: frame.ID, Errors: []errorMessage{{ErrorType: errorUnknownOp, Message: "unknown operation id " + frame.ID}}})
		return
	}
	unsubscribe()
	delete(c.subscriptions, frame.ID)
	c.reply(idFrame{Type: frameUnsubscribeSuccess, ID: frame.ID})
}

func (c *connection) reply(frame any) {
	c.queue.Offer(mustEncode(frame))
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
