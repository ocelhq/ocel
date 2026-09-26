package listeners_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
)

const tcpTable = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
   1: 0100007F:07E3 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12346 1 0000000000000000 100 0 0 10 0
   2: 0100007F:0016 0100007F:C1A8 01 00000000:00000000 00:00000000 00000000     0        0 12347 1 0000000000000000 100 0 0 10 0
`

const tcp6Table = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:01BB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 22222 1 0000000000000000 100 0 0 10 0
   1: 000080FE00000000FF565EFEA1B7C846:07E3 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 22223 1 0000000000000000 100 0 0 10 0
`

func TestATableNamesEveryListeningSocketAndNothingElse(t *testing.T) {
	t.Parallel()

	parsed, err := listeners.Parse(strings.NewReader(tcpTable))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if want := []string{"0.0.0.0:80", "127.0.0.1:2019"}; !slices.Equal(listeners.Lines(parsed), want) {
		t.Fatalf("Parse() = %v, want %v: an established connection is no listener", listeners.Lines(parsed), want)
	}
}

func TestASixteenByteAddressIsReadWordByWord(t *testing.T) {
	t.Parallel()

	parsed, err := listeners.Parse(strings.NewReader(tcp6Table))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	want := []string{"[::]:443", "[fe80::fe5e:56ff:46c8:b7a1]:2019"}
	if !slices.Equal(listeners.Lines(parsed), want) {
		t.Fatalf("Parse() = %v, want %v", listeners.Lines(parsed), want)
	}
}

func TestTheAdminPortIsFoundOnEitherFamily(t *testing.T) {
	t.Parallel()

	parsed, err := listeners.Parse(strings.NewReader(tcpTable + tcp6Table))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if len(parsed) != 4 {
		t.Fatalf("Parse() found %v, want every listening socket in both tables", listeners.Lines(parsed))
	}
	found := listeners.On(parsed, 2019)
	if want := []string{"127.0.0.1:2019", "[fe80::fe5e:56ff:46c8:b7a1]:2019"}; !slices.Equal(listeners.Lines(found), want) {
		t.Errorf("On(2019) = %v, want %v: a loopback bind is still a bind", listeners.Lines(found), want)
	}
	if len(listeners.On(parsed, 8080)) != 0 {
		t.Errorf("On(8080) = %v over a table that lists four listeners and none of them on 8080", listeners.Lines(listeners.On(parsed, 8080)))
	}
}

func TestALineNoTableEverWroteIsRefusedRatherThanRead(t *testing.T) {
	t.Parallel()

	for what, table := range map[string]string{
		"an address of no length any family uses": "  sl  local_address\n   0: 0000:0050 00000000:0000 0A 0 0 0\n",
		"a port that is not hex":                  "  sl  local_address\n   0: 00000000:zzzz 00000000:0000 0A 0 0 0\n",
		"an address that is not hex":              "  sl  local_address\n   0: zzzzzzzz:0050 00000000:0000 0A 0 0 0\n",
	} {
		if parsed, err := listeners.Parse(strings.NewReader(table)); err == nil {
			t.Errorf("Parse(%s) = %v, want a refusal rather than a listener read off a line nothing wrote", what, listeners.Lines(parsed))
		}
	}
}

func TestWhatTheHelperPrintsIsWhatTheBoxReadsBack(t *testing.T) {
	t.Parallel()

	parsed, err := listeners.Parse(strings.NewReader(tcpTable + tcp6Table))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	printed := strings.Join(listeners.Lines(parsed), "\n") + "\n"
	read, err := listeners.Read(printed)
	if err != nil {
		t.Fatalf("Read(%q) = %v", printed, err)
	}
	if !slices.Equal(listeners.Lines(read), listeners.Lines(parsed)) {
		t.Errorf("Read() = %v, want the %v the helper printed", listeners.Lines(read), listeners.Lines(parsed))
	}
	if _, err := listeners.Read("caddy is listening on port eighty\n"); err == nil {
		t.Error("Read() took a line no helper prints as a listener, so a proxy that answered something else would read as an empty netns")
	}
}

const socketsOwned = listeners.SocketsMark + `
/proc/812/fd socket:[12345]
/proc/813/fd socket:[12345]
/proc/900/fd socket:[22222]
/proc/77/fd socket:[99999]
` + listeners.NamesMark + `
/proc/812/comm:nginx
/proc/813/comm:nginx
/proc/900/comm:docker-proxy
/proc/77/comm:tmux: server
`

func TestAListenerIsNamedForTheProcessesThatOwnItsSocket(t *testing.T) {
	t.Parallel()

	parsed, err := listeners.Parse(strings.NewReader(tcpTable + tcp6Table + socketsOwned))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	for port, want := range map[int][]string{80: {"nginx"}, 443: {"docker-proxy"}} {
		if got := listeners.Owners(listeners.On(parsed, port)); !slices.Equal(got, want) {
			t.Errorf("Owners(On(%d)) = %v, want %v: nginx's master and its worker share one socket and are one owner", port, got, want)
		}
	}
	if got := listeners.Owners(listeners.On(parsed, 2019)); len(got) != 0 {
		t.Errorf("Owners(On(2019)) = %v over sockets no process in the reading owns, want none", got)
	}
}

func TestAHolderIsNamedAsTheKernelNamesIt(t *testing.T) {
	t.Parallel()

	table := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 99999 1 0000000000000000 100 0 0 10 0\n"
	parsed, err := listeners.Parse(strings.NewReader(table + socketsOwned))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if got := listeners.Owners(parsed); !slices.Equal(got, []string{"tmux: server"}) {
		t.Errorf("Owners() = %v, want the name with the space the kernel gave it", got)
	}
}

func TestALineNoReadingEverWroteNamesNoOwnerAndStopsNothing(t *testing.T) {
	t.Parallel()

	for what, said := range map[string]string{
		"a socket with no process":            listeners.SocketsMark + "\nsocket:[12345]\n" + listeners.NamesMark + "\n/proc/812/comm:nginx\n",
		"a socket that is no inode":           listeners.SocketsMark + "\n/proc/812/fd socket:[x]\n" + listeners.NamesMark + "\n/proc/812/comm:nginx\n",
		"a process that is no pid":            listeners.SocketsMark + "\n/proc/self/fd socket:[12345]\n" + listeners.NamesMark + "\n/proc/self/comm:nginx\n",
		"a name with no process":              listeners.SocketsMark + "\n/proc/812/fd socket:[12345]\n" + listeners.NamesMark + "\nnginx\n",
		"a name under no pid at all":          listeners.SocketsMark + "\n/proc/812/fd socket:[12345]\n" + listeners.NamesMark + "\n/proc/x/comm:nginx\n",
		"a name a process wrote a newline in": listeners.SocketsMark + "\n/proc/9/fd socket:[1]\n" + listeners.NamesMark + "\n/proc/9/comm:evil\nnginx\n",
	} {
		parsed, err := listeners.Parse(strings.NewReader(tcpTable + said))
		if err != nil {
			t.Errorf("Parse(%s) = %v, want the line passed over: a process any local user can name is no reason to stop a bootstrap", what, err)
			continue
		}
		if got := listeners.Owners(listeners.On(parsed, 80)); len(got) != 0 {
			t.Errorf("Parse(%s) named %v as listening on :80, off a line nothing that names an owner wrote", what, got)
		}
	}
}

func grepped(pid, comm string) string {
	var said strings.Builder
	for line := range strings.Lines(comm + "\n") {
		said.WriteString("/proc/" + pid + "/comm:" + line)
	}
	return said.String()
}

func TestAProcessThatNamesItselfWithANewlineIsNamedAsTheKernelRecordsItAndNamesNoOtherProcess(t *testing.T) {
	t.Parallel()

	for payload, want := range map[string][]string{
		"x\n" + listeners.SocketsMark: {"nginx", `x\n` + listeners.SocketsMark},
		"\n/proc/812/comm:X":          {`\n/proc/812/comm:X`, "nginx"},
		"x\n" + listeners.NamesMark:   {"nginx", `x\n` + listeners.NamesMark},
	} {
		said := listeners.SocketsMark + "\n/proc/812/fd socket:[12345]\n/proc/9/fd socket:[12345]\n" +
			listeners.NamesMark + "\n" + grepped("812", "nginx") + grepped("9", payload) + grepped("10", "sshd")
		parsed, err := listeners.Parse(strings.NewReader(tcpTable + said))
		if err != nil {
			t.Errorf("Parse() over a process named %q = %v", payload, err)
			continue
		}
		if got := listeners.Owners(listeners.On(parsed, 80)); !slices.Equal(got, want) {
			t.Errorf("Parse() over a process named %q names %q as listening on :80, want %q: any local user can name a process, and the refusal then tells the user to stop whatever that name says", payload, got, want)
		}
	}
}

func TestAMarkInsideALaterSectionNeverReopensAnEarlierOne(t *testing.T) {
	t.Parallel()

	said := listeners.SocketsMark + "\n/proc/812/fd socket:[12345]\n" +
		listeners.NamesMark + "\n/proc/812/comm:nginx\n" +
		listeners.SocketsMark + "\n/proc/9/fd socket:[12345]\n" +
		listeners.NamesMark + "\n/proc/9/comm:evil\n"
	parsed, err := listeners.Parse(strings.NewReader(tcpTable + said))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if got := listeners.Owners(listeners.On(parsed, 80)); !slices.Equal(got, []string{"nginx"}) {
		t.Errorf("Owners(On(80)) = %v, want only nginx: a mark past the names reopened the sockets", got)
	}
}
