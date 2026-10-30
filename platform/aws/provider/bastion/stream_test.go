package bastion_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/gorilla/websocket"
	"github.com/mmmorris1975/ssm-session-client/datachannel"
	"go.opentelemetry.io/otel/codes"

	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
)

func TestOpeningASessionIsASpanNamingTheNodeAndTheHostItForwardsTo(t *testing.T) {
	ctx, recorded := tracingtest.Record(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"__type":"TargetNotConnected","message":"not connected"}`))
	}))
	t.Cleanup(server.Close)
	cfg := aws.Config{
		Region:       "eu-west-2",
		BaseEndpoint: aws.String(server.URL),
		Credentials:  credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "example-secret", ""),
		HTTPClient:   sdkconfig.NewTracedClient(nil),
	}

	if _, err := bastion.OpenSession(cfg)(ctx, "ecs:cluster_task_runtime", "db.internal", 5432); err == nil {
		t.Fatal("OpenSession succeeded against a refusing endpoint")
	}

	span := tracingtest.SpanNamed(t, recorded.Ended(), "open bastion session")
	attrs := tracingtest.Attributes(span)
	want := map[string]string{"aws.ssm.target": "ecs:cluster_task_runtime", "server.address": "db.internal", "server.port": "5432"}
	for k, v := range want {
		if attrs[k].String() != v {
			t.Errorf("%s = %q, want %q", k, attrs[k].String(), v)
		}
	}
	if span.Status().Code != codes.Error {
		t.Errorf("status = %v, want an error", span.Status())
	}
	if len(tracingtest.ChildrenOf(recorded.Ended(), span)) == 0 {
		t.Error("the session's StartSession call left no span under open bastion session")
	}
}

func TestASessionSendsBytesAgainWhenTheAgentDropsThemWhileItIsStillConnectingToTheHost(t *testing.T) {
	agent := newPortForwardingAgent(t)

	stream, err := bastion.OpenSession(agent.config())(t.Context(), "ecs:cluster_task_runtime", "db.internal", 5432)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	t.Cleanup(func() { _ = stream.Terminate() })
	if err := stream.Send([]byte{0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	answered := make(chan []byte, 1)
	go func() {
		payload, _ := stream.Receive()
		answered <- payload
	}()
	select {
	case payload := <-answered:
		if string(payload) != "S" {
			t.Fatalf("Receive = %q, want the host's answer %q", payload, "S")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the host never answered: the bytes the agent dropped before it reached the host were never sent again")
	}
}

type portForwardingAgent struct {
	t      *testing.T
	server *httptest.Server
}

func newPortForwardingAgent(t *testing.T) *portForwardingAgent {
	agent := &portForwardingAgent{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/stream", agent.serveDataChannel)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"SessionId":  "session-1",
			"StreamUrl":  "ws" + strings.TrimPrefix(agent.server.URL, "http") + "/stream",
			"TokenValue": "token",
		})
	})
	agent.server = httptest.NewServer(mux)
	t.Cleanup(agent.server.Close)
	return agent
}

func (a *portForwardingAgent) config() aws.Config {
	return aws.Config{
		Region:       "eu-west-2",
		BaseEndpoint: aws.String(a.server.URL),
		Credentials:  credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "example-secret", ""),
	}
}

func (a *portForwardingAgent) serveDataChannel(w http.ResponseWriter, r *http.Request) {
	conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	var opening map[string]string
	if err := conn.ReadJSON(&opening); err != nil {
		return
	}
	a.send(conn, 0, datachannel.HandshakeRequest, []byte(`{"AgentVersion":"3.3.0.0","RequestedClientActions":[{"ActionType":"SessionType","ActionParameters":{"SessionType":"Port"}}]}`))
	outgoing := int64(1)
	expected := int64(0)
	connectedToHost := false
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		msg := new(datachannel.AgentMessage)
		if err := msg.UnmarshalBinary(raw); err != nil {
			a.t.Errorf("the agent read a malformed message: %v", err)
			return
		}
		if msg.MessageType != datachannel.InputStreamData {
			continue
		}
		if msg.SequenceNumber < expected {
			a.acknowledge(conn, msg)
			continue
		}
		if msg.SequenceNumber > expected {
			continue
		}
		switch msg.PayloadType {
		case datachannel.HandshakeResponse:
			a.acknowledge(conn, msg)
			expected++
			a.send(conn, outgoing, datachannel.HandshakeComplete, []byte(`{}`))
			outgoing++
		case datachannel.Output:
			if !connectedToHost {
				connectedToHost = true
				continue
			}
			a.acknowledge(conn, msg)
			expected++
			a.send(conn, outgoing, datachannel.Output, []byte("S"))
			outgoing++
		}
	}
}

func (a *portForwardingAgent) send(conn *websocket.Conn, sequence int64, payloadType datachannel.PayloadType, payload []byte) {
	msg := datachannel.NewAgentMessage()
	msg.MessageType = datachannel.OutputStreamData
	msg.SequenceNumber = sequence
	msg.PayloadType = payloadType
	msg.Payload = payload
	a.write(conn, msg)
}

func (a *portForwardingAgent) acknowledge(conn *websocket.Conn, received *datachannel.AgentMessage) {
	payload, _ := json.Marshal(map[string]any{
		"AcknowledgedMessageType":           received.MessageType,
		"AcknowledgedMessageSequenceNumber": received.SequenceNumber,
		"IsSequentialMessage":               true,
	})
	msg := datachannel.NewAgentMessage()
	msg.MessageType = datachannel.Acknowledge
	msg.SequenceNumber = received.SequenceNumber
	msg.Flags = datachannel.Ack
	msg.Payload = payload
	a.write(conn, msg)
}

func (a *portForwardingAgent) write(conn *websocket.Conn, msg *datachannel.AgentMessage) {
	data, err := msg.MarshalBinary()
	if err != nil {
		a.t.Errorf("marshal the agent's message: %v", err)
		return
	}
	_ = conn.WriteMessage(websocket.BinaryMessage, data)
}
