//go:build unix

package cli

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

const descendantMaxPIDs = 4096

func descendantsOf(parents map[int]int, pid int) []int {
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
			if len(out) >= descendantMaxPIDs {
				return out
			}
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

func parsePPIDTable(r io.Reader) map[int]int {
	table := map[int]int{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		table[pid] = ppid
	}
	return table
}
