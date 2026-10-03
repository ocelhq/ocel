package logview

import (
	"regexp"
	"strings"
)

type trace int

const (
	plain trace = iota
	pythonBody
	goTrace
	rustPanic
	rustMessage
	rustTrace
)

var (
	nodeFrame  = regexp.MustCompile(`^\s+(?:at |File ")`)
	goroutine  = regexp.MustCompile(`^goroutine \d+ \[`)
	goFrame    = regexp.MustCompile(`^[\w./*()-]+\(.*\)$`)
	rustThread = regexp.MustCompile(`^thread '.*' panicked at `)
)

func Join(lines []string) [][]string {
	var groups [][]string
	mode := plain
	for _, line := range lines {
		next, joins := step(mode, line)
		if joins && len(groups) > 0 {
			last := len(groups) - 1
			groups[last] = append(groups[last], line)
		} else {
			groups = append(groups, []string{line})
		}
		mode = next
	}
	return groups
}

func step(mode trace, line string) (trace, bool) {
	switch {
	case strings.HasPrefix(line, "Traceback (most recent call last):"):
		return pythonBody, false
	case goroutine.MatchString(line):
		return goTrace, mode == goTrace
	case strings.HasPrefix(line, "panic: "), strings.HasPrefix(line, "fatal error: "):
		return goTrace, false
	case rustThread.MatchString(line):
		return rustPanic, false
	case line == "stack backtrace:":
		return rustTrace, mode == rustPanic || mode == rustMessage || mode == rustTrace
	}

	indented := strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
	switch mode {
	case pythonBody:
		switch {
		case line == "":
			return plain, false
		case indented:
			return pythonBody, true
		}
		return plain, true
	case goTrace:
		if line == "" || indented || goFrame.MatchString(line) ||
			strings.HasPrefix(line, "created by ") || strings.HasPrefix(line, "exit status ") ||
			strings.HasPrefix(line, "[signal ") {
			return goTrace, true
		}
		return plain, false
	case rustPanic:
		if line == "" {
			return plain, false
		}
		if strings.HasPrefix(line, "note: ") {
			return plain, true
		}
		return rustMessage, true
	case rustMessage:
		return plain, strings.HasPrefix(line, "note: ")
	case rustTrace:
		if indented {
			return rustTrace, true
		}
		return plain, strings.HasPrefix(line, "note: ")
	}
	return plain, nodeFrame.MatchString(line)
}
