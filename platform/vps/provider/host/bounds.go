package host

import (
	"strconv"
	"time"
)

const (
	pullAttemptSeconds = 300
	dockerStepSeconds  = 120
	readySeconds       = 120
	probeSeconds       = 10
	storeCallSeconds   = 75
	routingLockSeconds = 300

	killGraceSeconds = 10
)

func within(seconds int, command string) string {
	return "timeout -k " + strconv.Itoa(killGraceSeconds) + " " + strconv.Itoa(seconds) + " " + command
}

func spelledSeconds(seconds int) string { return (time.Duration(seconds) * time.Second).String() }

func stall(what string, seconds int) string {
	return what + " did not finish within " + spelledSeconds(seconds)
}

func stalled(what string, seconds int) string {
	return "case \"$status\" in 124|137) printf '%s\\n' " + quoted(stall(what, seconds)) + " >&2 ;; esac"
}

func boundedStep(seconds int, what, command string) string {
	return within(seconds, command) + " || { status=$?; " + stalled(what, seconds) + "; exit \"$status\"; }"
}

func boundedTolerated(seconds int, what, command string) string {
	return within(seconds, command) + " || { status=$?; " + stalled(what, seconds) +
		"; case \"$status\" in 124|137) exit \"$status\" ;; esac; }"
}

func lockedWithin(path, mode string, seconds int) string {
	return "exec 9<" + quoted(path) + "\n" +
		within(seconds, "flock "+mode+" 9") + " || { printf '%s\\n' " +
		quoted("the lock on "+path+" was held elsewhere for "+spelledSeconds(seconds)) + " >&2; exit 1; }\n"
}
