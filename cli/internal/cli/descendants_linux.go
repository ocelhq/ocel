//go:build linux

package cli

import (
	"os"
	"strconv"
	"strings"
)

const descendantMaxPIDs = 4096

func descendantPIDs(pid int) []int {
	parents := ppidTable(descendantMaxPIDs)
	if len(parents) == 0 {
		return nil
	}

	children := make(map[int][]int, len(parents))
	for child, parent := range parents {
		children[parent] = append(children[parent], child)
	}

	var out []int
	queue := []int{pid}
	seen := map[int]bool{pid: true}
	for len(queue) > 0 && len(out) < descendantMaxPIDs {
		next := queue[0]
		queue = queue[1:]
		for _, c := range children[next] {
			if seen[c] {
				continue
			}
			seen[c] = true
			out = append(out, c)
			queue = append(queue, c)
		}
	}
	return out
}

func ppidTable(limit int) map[int]int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}

	table := make(map[int]int, len(entries))
	for _, e := range entries {
		if len(table) >= limit {
			break
		}
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
