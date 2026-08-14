//go:build linux

package cli

import (
	"os"
	"strconv"
	"strings"
)

func descendantPIDs(pid int) []int {
	return descendantsOf(ppidTable(), pid)
}

func ppidTable() map[int]int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}

	table := make(map[int]int, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		ppid, ok := readPPID(pid)
		if !ok {
			continue
		}
		table[pid] = ppid
	}
	return table
}

func readPPID(pid int) (int, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	s := string(raw)
	close := strings.LastIndexByte(s, ')')
	if close < 0 || close+2 >= len(s) {
		return 0, false
	}
	fields := strings.Fields(s[close+2:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}
