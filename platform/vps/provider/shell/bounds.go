package shell

import (
	"strconv"
	"time"
)

const (
	PullAttemptSeconds = 120
	PullBudgetSeconds  = 300
	DockerStepSeconds  = 120
	ReadySeconds       = 120
	ProbeSeconds       = 10
	StoreCallSeconds   = 75
	RoutingLockSeconds = 300

	KillGraceSeconds = 10
)

func Within(seconds int, command string) string {
	return "timeout -k " + strconv.Itoa(KillGraceSeconds) + " " + strconv.Itoa(seconds) + " " + command
}

func Seconds(seconds int) string { return (time.Duration(seconds) * time.Second).String() }

func stall(what string, seconds int) string {
	return what + " did not finish within " + Seconds(seconds)
}

func stalled(what string, seconds int) string {
	return "case \"$status\" in 124|137) printf '%s\\n' " + Quote(stall(what, seconds)) + " >&2 ;; esac"
}

func Bounded(seconds int, what, command string) string {
	return Within(seconds, command) + " || { status=$?; " + stalled(what, seconds) + "; exit \"$status\"; }"
}

func BoundedTolerating(seconds int, what, command string) string {
	return Within(seconds, command) + " || { status=$?; " + stalled(what, seconds) +
		"; case \"$status\" in 124|137) exit \"$status\" ;; esac; }"
}

func Locked(path, mode string, seconds int) string {
	return "exec 9<" + Quote(path) + "\n" +
		Within(seconds, "flock "+mode+" 9") + " || { printf '%s\\n' " +
		Quote("the lock on "+path+" was held elsewhere for "+Seconds(seconds)) + " >&2; exit 1; }\n"
}
