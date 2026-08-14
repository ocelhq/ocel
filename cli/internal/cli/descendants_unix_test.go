//go:build unix

package cli

import (
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestParsePPIDTableSkipsMalformedLines(t *testing.T) {
	const out = `  101   1
  202 101
header line
  303 abc

  404   202
`
	table := parsePPIDTable(strings.NewReader(out))

	want := map[int]int{101: 1, 202: 101, 404: 202}
	if len(table) != len(want) {
		t.Fatalf("table = %v, want %v", table, want)
	}
	for pid, ppid := range want {
		if table[pid] != ppid {
			t.Fatalf("table[%d] = %d, want %d", pid, table[pid], ppid)
		}
	}
}

func TestDescendantsOfWalksWholeTree(t *testing.T) {
	table := map[int]int{
		1:   0,
		101: 1,
		202: 101,
		303: 202,
		404: 101,
		505: 1,
	}

	got := descendantsOf(table, 101)
	sort.Ints(got)

	want := []int{202, 303, 404}
	if len(got) != len(want) {
		t.Fatalf("descendantsOf = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("descendantsOf = %v, want %v", got, want)
		}
	}
}

func TestDescendantsOfIgnoresParentCycles(t *testing.T) {
	table := map[int]int{101: 202, 202: 101}

	got := descendantsOf(table, 101)
	if len(got) != 1 || got[0] != 202 {
		t.Fatalf("descendantsOf = %v, want [202]", got)
	}
}

func TestDescendantsOfBoundsOutput(t *testing.T) {
	table := make(map[int]int, descendantMaxPIDs*2)
	for pid := 2; pid < descendantMaxPIDs*2; pid++ {
		table[pid] = 1
	}

	if got := len(descendantsOf(table, 1)); got > descendantMaxPIDs {
		t.Fatalf("descendantsOf returned %d pids, want at most %d", got, descendantMaxPIDs)
	}
}

func TestDescendantsOfEmptyTable(t *testing.T) {
	if got := descendantsOf(nil, 101); got != nil {
		t.Fatalf("descendantsOf = %v, want nil", got)
	}
}

func TestPSSnapshotParsesIntoATable(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("ps unavailable")
	}

	out, err := exec.Command("ps", "-axo", "pid=,ppid=").Output()
	if err != nil {
		t.Skipf("ps snapshot failed: %v", err)
	}

	table := parsePPIDTable(strings.NewReader(string(out)))
	if len(table) == 0 {
		t.Fatal("parsePPIDTable found no processes in a ps snapshot")
	}
	if _, ok := table[1]; !ok {
		t.Fatal("parsePPIDTable found no pid 1 in a ps snapshot")
	}
}

func TestDescendantPIDsFindsAGrandchild(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sh -c 'sleep 5' & wait")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)
	for {
		if len(descendantPIDs(cmd.Process.Pid)) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("descendantPIDs never saw the grandchild")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
