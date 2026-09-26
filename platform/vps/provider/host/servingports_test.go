package host

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

type socketHeld struct {
	port   int
	holder string
}

func socketsSaid(held ...socketHeld) string {
	var table, sockets, names strings.Builder
	table.WriteString("  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n")
	for at, one := range held {
		inode := 40000 + at
		fmt.Fprintf(&table, "   %d: 00000000:%04X 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 %d 1 0000000000000000 100 0 0 10 0\n",
			at, one.port, inode)
		if one.holder == "" {
			continue
		}
		fmt.Fprintf(&sockets, "/proc/%d/fd socket:[%d]\n", 700+at, inode)
		fmt.Fprintf(&names, "/proc/%d/comm:%s\n", 700+at, one.holder)
	}
	return table.String() + listeners.SocketsMark + "\n" + sockets.String() + listeners.NamesMark + "\n" + names.String()
}

func portsHeldOn(stood *bench, published map[string]string, held ...socketHeld) {
	prior := stood.answer
	stood.answer = func(command string) (session.Result, bool) {
		for port, names := range published {
			if strings.Contains(command, "publish="+port) {
				return session.Result{Stdout: names}, true
			}
		}
		if strings.HasPrefix(command, holdersCommand) {
			return session.Result{Stdout: socketsSaid(held...)}, true
		}
		if prior != nil {
			return prior(command)
		}
		return session.Result{}, false
	}
}

func freshBox() *bench {
	fresh := machine(nil)
	fresh.answer = func(command string) (session.Result, bool) {
		return session.Result{Stdout: aKey + "\n"}, command == "cat ~/.ssh/authorized_keys 2>/dev/null"
	}
	return fresh
}

func withDocker() *bench {
	stood := machine(map[edge.Class][]Item{edge.ClassProduction: EngineItems()})
	stood.answer = func(command string) (session.Result, bool) {
		return session.Result{Stdout: aKey + "\n"}, command == "cat ~/.ssh/authorized_keys 2>/dev/null"
	}
	return stood
}

const behindYourOwnProxy = "See https://ocel.dev/docs/providers/vps#behind-your-own-proxy"

func refusedBeforeWriting(t *testing.T, stood *bench, front Front, want string) {
	t.Helper()

	class := edge.ClassProduction
	boot := NewBootstrap(stood.fronted(front), testVendor, "shop")
	_, planned := boot.Plan(context.Background(), provider.BootstrapRequest{Class: class})
	applied := boot.Apply(context.Background(), provider.BootstrapRequest{Class: class, WrittenBy: "the-suite"}, nil)
	for step, err := range map[string]error{"Plan": planned, "Apply": applied} {
		if refused := refusalOf(t, err, refusal.CodeNotReady); refused.Message != want {
			t.Errorf("%s refused with\n%s\nwant\n%s", step, refused.Message, want)
		}
	}
	for _, command := range stood.commands() {
		if strings.HasPrefix(command, "install ") || strings.Contains(command, dockerSource) || strings.Contains(command, "docker run") {
			t.Errorf("the refused bootstrap still wrote: %s", command)
		}
	}
}

func TestABootstrapOverAProcessHoldingTheServingPortsNamesItAndStopsBeforeItsFirstWrite(t *testing.T) {
	t.Parallel()

	stood := freshBox()
	portsHeldOn(stood, nil, socketHeld{80, "nginx"}, socketHeld{443, "nginx"})
	refusedBeforeWriting(t, stood, Front{},
		"nginx holds :80 and :443, where ocel's own proxy serves\n"+
			"Add `\"proxy\": \"manual\"` to this project's vps options and route to ocel from nginx, or stop nginx and run `ocel bootstrap production`\n"+
			behindYourOwnProxy)
}

func TestABootstrapOverAContainerPublishingAServingPortNamesTheContainer(t *testing.T) {
	t.Parallel()

	stood := withDocker()
	portsHeldOn(stood, map[string]string{caddy.HTTPPort: "\n", "443": "web\n"}, socketHeld{443, "docker-proxy"})
	refusedBeforeWriting(t, stood, Front{},
		"container web publishes :443, where ocel's own proxy serves\n"+
			"Add `\"proxy\": \"manual\"` to this project's vps options and route to ocel from web, or run `docker rm -f web` and run `ocel bootstrap production`\n"+
			behindYourOwnProxy)
}

func TestABootstrapNamesEveryHolderOfTheServingPortsInOneRefusal(t *testing.T) {
	t.Parallel()

	stood := freshBox()
	portsHeldOn(stood, nil, socketHeld{80, "nginx"}, socketHeld{443, "apache2"})
	refusedBeforeWriting(t, stood, Front{},
		"nginx holds :80 and apache2 holds :443, where ocel's own proxy serves\n"+
			"Add `\"proxy\": \"manual\"` to this project's vps options and route to ocel from nginx and apache2, or stop nginx and apache2 and run `ocel bootstrap production`\n"+
			behindYourOwnProxy)
}

func TestABootstrapOverASocketNoProcessCanBeNamedForSaysWhereItIsBound(t *testing.T) {
	t.Parallel()

	stood := freshBox()
	portsHeldOn(stood, nil, socketHeld{80, ""})
	refusedBeforeWriting(t, stood, Front{},
		"the process at 0.0.0.0:80 holds :80, where ocel's own proxy serves\n"+
			"Add `\"proxy\": \"manual\"` to this project's vps options and route to ocel from the process at 0.0.0.0:80, or stop the process at 0.0.0.0:80 and run `ocel bootstrap production`\n"+
			behindYourOwnProxy)
}

func TestABootstrapWhoseServingPortsAreFreeOrOcelsOwnGoesAhead(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	for name, stood := range map[string]*bench{
		"a fresh box":                   freshBox(),
		"a box ocel's own proxy fronts": settledOn(t, class),
	} {
		portsHeldOn(stood, map[string]string{caddy.HTTPPort: caddy.Container + "\n", "443": caddy.Container + "\n"})
		if _, err := NewBootstrap(stood.host(), testVendor, "shop").Plan(context.Background(), provider.BootstrapRequest{Class: class}); err != nil {
			t.Errorf("%s: Plan() = %v, want the bootstrap let through", name, err)
		}
	}
}

func TestABootstrapBehindYourOwnProxyLeavesWhatHoldsTheServingPortsAlone(t *testing.T) {
	t.Parallel()

	stood := freshBox()
	portsHeldOn(stood, nil, socketHeld{80, "nginx"}, socketHeld{443, "nginx"})
	if _, err := NewBootstrap(stood.fronted(routedByHand()), testVendor, "shop").Plan(context.Background(),
		provider.BootstrapRequest{Class: edge.ClassProduction}); err != nil {
		t.Fatalf("Plan() = %v, want a box routed by hand free to keep its own proxy on 80 and 443", err)
	}
	if slices.ContainsFunc(stood.commands(), func(command string) bool { return strings.HasPrefix(command, holdersCommand) }) {
		t.Error("a bootstrap behind your own proxy asked what holds the serving ports, and it holds them by design")
	}
}

func TestTheHoldersReadNamesTheProcessBehindASocketOnThisMachine(t *testing.T) {
	t.Parallel()

	listening, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listening.Close() })
	port := listening.Addr().(*net.TCPAddr).Port

	said, err := exec.Command("/bin/sh", "-c", holdersCommand).Output()
	if err != nil {
		t.Fatalf("the holders read on this machine = %v", err)
	}
	held, err := listeners.Parse(strings.NewReader(string(said)))
	if err != nil {
		t.Fatalf("the holders read on this machine parses as %v, so the kernel, find and grep write something the parser never expected", err)
	}
	comm, err := os.ReadFile("/proc/self/comm")
	if err != nil {
		t.Skipf("this machine has no /proc to name a process from: %v", err)
	}
	if got, want := listeners.Holders(listeners.On(held, port)), []string{strings.TrimSpace(string(comm))}; !slices.Equal(got, want) {
		t.Errorf("the socket this test holds on :%d is named %v, want %v", port, got, want)
	}
}

func heldByAProcessNamed(t *testing.T, name string) int {
	t.Helper()
	listening, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listening.Close() })
	socket, err := listening.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	child := exec.Command("/bin/sh", "-c", `printf '%s' "$1" >/proc/$$/comm && echo named && read held`, "sh", name)
	child.ExtraFiles = []*os.File{socket}
	held, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	said, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		held.Close()
		child.Wait()
	})
	if _, err := fmt.Fscanln(said, new(string)); err != nil {
		t.Skipf("this machine will not let a process name itself: %v", err)
	}
	return listening.Addr().(*net.TCPAddr).Port
}

func TestTheHoldersReadNamesAProcessNamedLikeItsOwnLinesAsThatProcessAlone(t *testing.T) {
	t.Parallel()

	comm, err := os.ReadFile("/proc/self/comm")
	if err != nil {
		t.Skipf("this machine has no /proc to name a process from: %v", err)
	}
	for _, name := range []string{"x\n" + listeners.SocketsMark, "\n/proc/1/comm:X"} {
		port := heldByAProcessNamed(t, name)
		said, err := exec.Command("/bin/sh", "-c", holdersCommand).Output()
		if err != nil {
			t.Fatalf("the holders read on this machine = %v", err)
		}
		held, err := listeners.Parse(strings.NewReader(string(said)))
		if err != nil {
			t.Fatalf("the holders read parses as %v over a process named %q", err, name)
		}
		want := []string{strings.TrimSpace(string(comm)), strings.ReplaceAll(name, "\n", `\n`)}
		slices.Sort(want)
		if got := listeners.Holders(listeners.On(held, port)); !slices.Equal(got, want) {
			t.Errorf("the socket on :%d held by this test and a process named %q is named %q, want %q", port, name, got, want)
		}
	}
}
