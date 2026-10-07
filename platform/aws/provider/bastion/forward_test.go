package bastion_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
)

type echoStream struct {
	replies    chan []byte
	terminated chan struct{}
	once       sync.Once
	sent       bytes.Buffer
	mu         sync.Mutex
}

func newEchoStream() *echoStream {
	return &echoStream{replies: make(chan []byte, 16), terminated: make(chan struct{})}
}

func (s *echoStream) Receive() ([]byte, error) {
	select {
	case payload := <-s.replies:
		return payload, nil
	case <-s.terminated:
		return nil, io.EOF
	}
}

func (s *echoStream) Send(payload []byte) error {
	s.mu.Lock()
	s.sent.Write(payload)
	s.mu.Unlock()
	s.replies <- append([]byte("echo:"), payload...)
	return nil
}

func (s *echoStream) Terminate() error {
	s.once.Do(func() { close(s.terminated) })
	return nil
}

type opened struct {
	target string
	host   string
	port   int
}

type sessions struct {
	mu      sync.Mutex
	opened  []opened
	streams []*echoStream
	failure error

	afterStop       func() bool
	afterStopOpened int
}

func (s *sessions) openedAfterStop() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.afterStopOpened
}

func (s *sessions) open(_ context.Context, target, host string, port int) (bastion.Stream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.afterStop != nil && s.afterStop() {
		s.afterStopOpened++
	}
	if s.failure != nil {
		return nil, s.failure
	}
	s.opened = append(s.opened, opened{target, host, port})
	stream := newEchoStream()
	s.streams = append(s.streams, stream)
	return stream, nil
}

func (s *sessions) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.opened)
}

var forwardTargets = []bastion.Target{
	{Name: "orders", Host: "orders.cluster-abc.us-east-1.rds.amazonaws.com", Port: 5432},
	{Name: "cache", Host: "master.cache.abc.use1.cache.amazonaws.com", Port: 6379},
}

func forwarded(t *testing.T, account *account, sess *sessions, report func(error)) []bastion.Forward {
	t.Helper()
	clients, ensured := readyBastion(t, account)
	forwards, err := ensured.Forward(context.Background(), clients, sess.open, report, forwardTargets)
	if err != nil {
		t.Fatalf("Forward() = %v", err)
	}
	return forwards
}

func roundTrip(t *testing.T, address, message string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", address, err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(message)); err != nil {
		t.Fatalf("write to %s: %v", address, err)
	}
	reply := make([]byte, len("echo:")+len(message))
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read from %s: %v", address, err)
	}
	return string(reply)
}

func TestForwardCarriesEachConnectionToItsTargetOverASessionOfItsOwnOnTheLoopback(t *testing.T) {
	t.Parallel()

	account, sess := newAccount(), &sessions{}
	forwards := forwarded(t, account, sess, nil)
	defer closeAll(forwards)

	if len(forwards) != 2 || forwards[0].Name != "orders" || forwards[1].Name != "cache" {
		t.Fatalf("Forward() = %+v, want orders then cache", forwards)
	}
	for _, forward := range forwards {
		if !strings.HasPrefix(forward.Address, "127.0.0.1:") {
			t.Errorf("%s listens on %s, want a loopback port: anything wider would hand the database to the network", forward.Name, forward.Address)
		}
	}
	if got := roundTrip(t, forwards[0].Address, "select 1"); got != "echo:select 1" {
		t.Errorf("a connection to orders got %q", got)
	}
	if got := roundTrip(t, forwards[0].Address, "select 2"); got != "echo:select 2" {
		t.Errorf("a second connection to orders got %q", got)
	}
	if got := roundTrip(t, forwards[1].Address, "PING"); got != "echo:PING" {
		t.Errorf("a connection to cache got %q", got)
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.opened) != 3 {
		t.Fatalf("sessions opened = %+v, want one per connection", sess.opened)
	}
	target := sess.opened[0].target
	if !strings.HasPrefix(target, "ecs:ocel-bastion-production_") {
		t.Errorf("session target = %q, want the bastion task", target)
	}
	counts := map[opened]int{}
	for _, o := range sess.opened {
		counts[o]++
	}
	if counts[opened{target, forwardTargets[0].Host, 5432}] != 2 || counts[opened{target, forwardTargets[1].Host, 6379}] != 1 {
		t.Errorf("sessions opened = %+v, want two to orders on 5432 and one to cache on 6379", sess.opened)
	}
}

func TestForwardKeepsTheTaskUntilTheLastForwardClosesThenStopsItAndEndsTheOpenSessions(t *testing.T) {
	t.Parallel()

	account, sess := newAccount(), &sessions{}
	forwards := forwarded(t, account, sess, nil)
	conn, err := net.Dial("tcp", forwards[0].Address)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, "a session for the open connection", func() bool { return sess.count() == 1 })

	forwards[0].Close()
	forwards[0].Close()
	if running := account.runningTasks(); len(running) != 1 {
		t.Errorf("tasks running after closing one of two forwards = %v, want the bastion to stay for the other", running)
	}
	waitFor(t, "the open session to end with its forward", func() bool {
		select {
		case <-sess.streams[0].terminated:
			return true
		default:
			return false
		}
	})
	forwards[1].Close()

	if running := account.runningTasks(); len(running) != 0 {
		t.Errorf("tasks %v still run after the last forward closed", running)
	}
	if _, err := net.DialTimeout("tcp", forwards[1].Address, time.Second); err == nil {
		t.Error("the cache forward still accepts connections after Close()")
	}
}

func TestForwardStopsTheTaskWhenItsContextEnds(t *testing.T) {
	t.Parallel()

	account, sess := newAccount(), &sessions{}
	clients, ensured := readyBastion(t, account)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := ensured.Forward(ctx, clients, sess.open, nil, forwardTargets); err != nil {
		t.Fatalf("Forward() = %v", err)
	}

	cancel()

	waitFor(t, "the task to stop once the context ended", func() bool { return len(account.runningTasks()) == 0 })
}

func TestForwardRefusesAPortTheBastionsSecurityGroupDoesNotOpenBeforeStartingATask(t *testing.T) {
	t.Parallel()

	account, sess := newAccount(), &sessions{}
	clients, ensured := readyBastion(t, account)

	_, err := ensured.Forward(context.Background(), clients, sess.open, nil, []bastion.Target{{Name: "other", Host: "10.0.0.5", Port: 22}})

	if err == nil {
		t.Fatal("Forward() to port 22 = nil error, want it refused: the security group lets nothing out on it")
	}
	if calls := account.called("RunTask"); len(calls) != 0 {
		t.Errorf("RunTask was called %d times for a forward that could never connect", len(calls))
	}
}

func TestForwardLeavesNoTaskBehindWhenTheTaskNeverGetsItsExecAgent(t *testing.T) {
	t.Parallel()

	account, sess := newAccount(), &sessions{}
	account.agentAfter = 100
	account.onDescribe = func(t *task) { t.stopped = true }
	clients, ensured := readyBastion(t, account)

	_, err := ensured.Forward(context.Background(), clients, sess.open, nil, forwardTargets)

	if err == nil {
		t.Fatal("Forward() = nil error, want the task's failure")
	}
	if running := account.runningTasks(); len(running) != 0 {
		t.Errorf("tasks %v still run", running)
	}
}

func TestForwardClosesAConnectionItCannotOpenASessionForAndSaysWhy(t *testing.T) {
	t.Parallel()

	account, sess := newAccount(), &sessions{failure: errors.New("TargetNotConnected")}
	reported := make(chan error, 1)
	forwards := forwarded(t, account, sess, func(err error) { reported <- err })
	defer closeAll(forwards)

	conn, err := net.Dial("tcp", forwards[0].Address)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Error("the connection stayed open though no session could carry it")
	}

	select {
	case err := <-reported:
		if !strings.Contains(err.Error(), "TargetNotConnected") || !strings.Contains(err.Error(), "orders") {
			t.Errorf("reported %q, want the target's name and the cause", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("the failure was not reported")
	}
}

func TestForwardBindingsPointsEachBindingAtTheLoopbackPortOfItsHostAndPort(t *testing.T) {
	t.Parallel()

	account, sess := newAccount(), &sessions{}
	clients, ensured := readyBastion(t, account)

	forwards, err := ensured.ForwardBindings(context.Background(), clients, sess.open, nil, []provider.Binding{
		{Type: provider.BindingPostgres, Name: "orders", Properties: map[string]string{provider.PropertyHost: "orders.rds.internal", provider.PropertyPort: "5432"}},
		{Type: provider.BindingKV, Name: "cache", Properties: map[string]string{provider.PropertyHost: "cache.internal", provider.PropertyPort: "6379"}},
	})
	if err != nil {
		t.Fatalf("ForwardBindings() = %v", err)
	}
	defer func() {
		for _, forward := range forwards {
			forward.Close()
		}
	}()

	if len(forwards) != 2 || forwards[0].Binding != "orders" || forwards[1].Binding != "cache" || !strings.HasPrefix(forwards[0].LocalAddress, "127.0.0.1:") {
		t.Fatalf("ForwardBindings() = %+v, want orders and cache each on a loopback port", forwards)
	}
	if got := roundTrip(t, forwards[1].LocalAddress, "PING"); got != "echo:PING" {
		t.Errorf("a connection to cache got %q", got)
	}
	if sess.opened[0].host != "cache.internal" || sess.opened[0].port != 6379 {
		t.Errorf("the session went to %s:%d, want cache.internal:6379", sess.opened[0].host, sess.opened[0].port)
	}
}

func TestForwardBindingsRefusesABindingThatNamesNoHostOrNoNumericPort(t *testing.T) {
	t.Parallel()

	account, sess := newAccount(), &sessions{}
	clients, ensured := readyBastion(t, account)
	for name, properties := range map[string]map[string]string{
		"no host":  {provider.PropertyPort: "5432"},
		"no port":  {provider.PropertyHost: "orders.rds.internal"},
		"bad port": {provider.PropertyHost: "orders.rds.internal", provider.PropertyPort: "five"},
	} {
		_, err := ensured.ForwardBindings(context.Background(), clients, sess.open, nil, []provider.Binding{{Type: provider.BindingPostgres, Name: "orders", Properties: properties}})
		if err == nil {
			t.Errorf("%s: ForwardBindings() = nil error, want it refused", name)
		}
	}
	if calls := account.called("RunTask"); len(calls) != 0 {
		t.Errorf("RunTask was called %d times for bindings that could never be forwarded", len(calls))
	}
}

func TestForwardOpensNoSessionOnceItsTaskIsStoppedHoweverManyConnectionsArriveAsItCloses(t *testing.T) {
	t.Parallel()

	for range 20 {
		account := newAccount()
		sess := &sessions{}
		var late sync.WaitGroup
		forwards := forwarded(t, account, sess, nil)
		sess.mu.Lock()
		sess.afterStop = func() bool { return len(account.runningTasks()) == 0 }
		sess.mu.Unlock()
		dialing := make(chan struct{})
		for range 48 {
			late.Add(1)
			go func() {
				defer late.Done()
				<-dialing
				if conn, err := net.Dial("tcp", forwards[0].Address); err == nil {
					_, _ = conn.Write([]byte("x"))
					defer conn.Close()
					time.Sleep(time.Millisecond)
				}
			}()
		}
		close(dialing)
		closeAll(forwards)
		late.Wait()
		time.Sleep(5 * time.Millisecond)

		if opened := sess.openedAfterStop(); opened > 0 {
			t.Fatalf("%d sessions were opened after the forwards closed and their task stopped", opened)
		}
	}
}

func closeAll(forwards []bastion.Forward) {
	for _, forward := range forwards {
		forward.Close()
	}
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
