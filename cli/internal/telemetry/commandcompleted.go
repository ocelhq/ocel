package telemetry

import "time"

type CommandCompletion struct {
	Command        string
	Flags          []string
	ExitCode       int
	ErrorCode      string
	Duration       time.Duration
	JSON           bool
	TTY            bool
	SkillInstalled bool
}

func (CommandCompletion) name() string { return "command_completed" }

func (completion CommandCompletion) properties() map[string]any {
	flags := completion.Flags
	if flags == nil {
		flags = []string{}
	}
	return map[string]any{
		"command":         completion.Command,
		"flags":           flags,
		"exit_code":       completion.ExitCode,
		"error_code":      completion.ErrorCode,
		"duration_ms":     completion.Duration.Milliseconds(),
		"json":            completion.JSON,
		"tty":             completion.TTY,
		"skill_installed": completion.SkillInstalled,
	}
}
