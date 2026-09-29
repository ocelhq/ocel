//go:build unix

package childprocess

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

const descendantMaxPIDs = 4096

func terminateTree(cmd *exec.Cmd) error {
	return signalTree(cmd, syscall.SIGTERM)
}

func killTree(cmd *exec.Cmd) error {
	return signalTree(cmd, syscall.SIGKILL)
}

func signalTree(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	pid := cmd.Process.Pid
	pids := append([]int{pid}, descendantPIDs(pid)...)

	var firstErr error
	delivered := false
	for _, p := range pids {
		err := syscall.Kill(p, sig)
		switch {
		case err == nil:
			delivered = true
		case errors.Is(err, syscall.ESRCH):
		case firstErr == nil:
			firstErr = err
		}
	}
	if firstErr != nil {
		return firstErr
	}
	if !delivered {
		return os.ErrProcessDone
	}
	return nil
}

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
