package telemetry

import "time"

type CommandCompletion struct {
	Command   string
	Flags     []string
	ExitCode  int
	ErrorCode string
	Duration  time.Duration
	JSON      bool
	TTY       bool
}

func NewCommandCompleted(identity Identity, at time.Time, completion CommandCompletion) (Event, error) {
	flags := completion.Flags
	if flags == nil {
		flags = []string{}
	}
	return newEvent("command_completed", identity, at, map[string]any{
		"command":     completion.Command,
		"flags":       flags,
		"exit_code":   completion.ExitCode,
		"error_code":  completion.ErrorCode,
		"duration_ms": completion.Duration.Milliseconds(),
		"json":        completion.JSON,
		"tty":         completion.TTY,
	})
}
