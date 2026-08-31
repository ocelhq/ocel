package listeners_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
)

const tcpTable = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
   1: 0100007F:07E3 00000000:0000 0A 00000000:00000000 00:00000000     0        0 12346 1 0000000000000000 100 0 0 10 0
   2: 0100007F:0016 0100007F:C1A8 01 00000000:00000000 00:00000000     0        0 12347 1 0000000000000000 100 0 0 10 0
`

const tcp6Table = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:01BB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000     0        0 22222 1 0000000000000000 100 0 0 10 0
   1: 000080FE00000000FF565EFEA1B7C846:07E3 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000     0        0 22223 1 0000000000000000 100 0 0 10 0
`

func TestATableNamesEveryListeningSocketAndNothingElse(t *testing.T) {
	t.Parallel()

	held, err := listeners.Parse(strings.NewReader(tcpTable))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if want := []string{"0.0.0.0:80", "127.0.0.1:2019"}; !slices.Equal(listeners.Lines(held), want) {
		t.Fatalf("Parse() = %v, want %v: an established connection is no listener", listeners.Lines(held), want)
	}
}

func TestASixteenByteAddressIsReadWordByWord(t *testing.T) {
	t.Parallel()

	held, err := listeners.Parse(strings.NewReader(tcp6Table))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	want := []string{"[::]:443", "[fe80::fe5e:56ff:46c8:b7a1]:2019"}
	if !slices.Equal(listeners.Lines(held), want) {
		t.Fatalf("Parse() = %v, want %v", listeners.Lines(held), want)
	}
}

func TestTheAdminPortIsFoundOnEitherFamily(t *testing.T) {
	t.Parallel()

	held, err := listeners.Parse(strings.NewReader(tcpTable + tcp6Table))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	if len(held) != 4 {
		t.Fatalf("Parse() found %v, want every listening socket in both tables", listeners.Lines(held))
	}
	found := listeners.On(held, 2019)
	if want := []string{"127.0.0.1:2019", "[fe80::fe5e:56ff:46c8:b7a1]:2019"}; !slices.Equal(listeners.Lines(found), want) {
		t.Errorf("On(2019) = %v, want %v: a loopback bind is still a bind", listeners.Lines(found), want)
	}
	if len(listeners.On(held, 8080)) != 0 {
		t.Errorf("On(8080) = %v over a table that holds four listeners and none of them on 8080", listeners.Lines(listeners.On(held, 8080)))
	}
}

func TestALineNoTableEverWroteIsRefusedRatherThanRead(t *testing.T) {
	t.Parallel()

	for what, table := range map[string]string{
		"an address of no length any family uses": "  sl  local_address\n   0: 0000:0050 00000000:0000 0A 0 0 0\n",
		"a port that is not hex":                  "  sl  local_address\n   0: 00000000:zzzz 00000000:0000 0A 0 0 0\n",
		"an address that is not hex":              "  sl  local_address\n   0: zzzzzzzz:0050 00000000:0000 0A 0 0 0\n",
	} {
		if held, err := listeners.Parse(strings.NewReader(table)); err == nil {
			t.Errorf("Parse(%s) = %v, want a refusal rather than a listener read off a line nothing wrote", what, listeners.Lines(held))
		}
	}
}

func TestWhatTheHelperPrintsIsWhatTheBoxReadsBack(t *testing.T) {
	t.Parallel()

	held, err := listeners.Parse(strings.NewReader(tcpTable + tcp6Table))
	if err != nil {
		t.Fatalf("Parse() = %v", err)
	}
	printed := strings.Join(listeners.Lines(held), "\n") + "\n"
	read, err := listeners.Read(printed)
	if err != nil {
		t.Fatalf("Read(%q) = %v", printed, err)
	}
	if !slices.Equal(listeners.Lines(read), listeners.Lines(held)) {
		t.Errorf("Read() = %v, want the %v the helper printed", listeners.Lines(read), listeners.Lines(held))
	}
	if _, err := listeners.Read("caddy is listening on port eighty\n"); err == nil {
		t.Error("Read() took a line no helper prints as a listener, so a proxy that answered something else would read as an empty netns")
	}
}
