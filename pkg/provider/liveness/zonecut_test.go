package liveness

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

const (
	flagResponse      = 0x8000
	flagAuthoritative = 0x0400
	rcodeNameError    = 3
)

func nameserverAnswering(t *testing.T, flags, answers, authorities uint16) (*Net, <-chan []byte) {
	t.Helper()
	listening, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listening.Close() })
	asked := make(chan []byte, 1)
	go func() {
		query := make([]byte, 512)
		n, from, err := listening.ReadFrom(query)
		if err != nil {
			return
		}
		asked <- append([]byte(nil), query[:n]...)
		reply := append([]byte(nil), query[:n]...)
		binary.BigEndian.PutUint16(reply[2:], flags)
		binary.BigEndian.PutUint16(reply[6:], answers)
		binary.BigEndian.PutUint16(reply[8:], authorities)
		_, _ = listening.WriteTo(reply, from)
	}()
	at := listening.LocalAddr().String()
	return &Net{Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "ns.example.net:53" {
			t.Errorf("the zone cut was asked of %s, want the nameserver named", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, at)
	}}, asked
}

func askZoneCut(t *testing.T, probe *Net) (bool, error) {
	t.Helper()
	ctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	return probe.authority("ns.example.net").IsZoneCut(ctx, "shop.example.com")
}

func TestAReferralFromTheParentsNameserverMarksAZoneCut(t *testing.T) {
	t.Parallel()

	probe, asked := nameserverAnswering(t, flagResponse, 0, 2)
	cut, err := askZoneCut(t, probe)
	if err != nil || !cut {
		t.Fatalf("IsZoneCut() = %v, %v, want a referral to name a zone cut", cut, err)
	}
	query := <-asked
	question := query[12:]
	want := append([]byte("\x04shop\x07example\x03com\x00"), 0, 2, 0, 1)
	if string(question) != string(want) {
		t.Errorf("the nameserver was asked %q, want an NS question for shop.example.com", question)
	}
	if flags := binary.BigEndian.Uint16(query[2:]); flags != 0 {
		t.Errorf("the query carries flags %#04x, want none: a nameserver asked for recursion may answer from its cache rather than say whom it delegates to", flags)
	}
}

func TestANameTheParentsNameserverAnswersForItselfIsNoZoneCut(t *testing.T) {
	t.Parallel()

	for name, flags := range map[string]uint16{
		"no such name": flagResponse | flagAuthoritative | rcodeNameError,
		"no NS record": flagResponse | flagAuthoritative,
	} {
		probe, _ := nameserverAnswering(t, flags, 0, 1)
		if cut, err := askZoneCut(t, probe); err != nil || cut {
			t.Errorf("IsZoneCut() over %s = %v, %v, want no zone cut", name, cut, err)
		}
	}
}
