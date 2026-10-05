package telemetry

import (
	"os"
	"strings"
)

const (
	EnvVar        = "OCEL_TELEMETRY"
	doNotTrackVar = "DO_NOT_TRACK"
	debugValue    = "debug"
)

var (
	WriteKey = ""
	Endpoint = ""
)

type Rule string

const (
	RuleDefault    Rule = "default"
	RuleNoKey      Rule = "no-key"
	RuleNoEndpoint Rule = "no-endpoint"
	RuleOptedOut   Rule = "opted-out"
	RuleDoNotTrack Rule = "do-not-track"
	RuleDebug      Rule = "debug"
)

type Resolution struct {
	Enabled bool
	Debug   bool
	Rule    Rule
}

func Resolve(key, endpoint string) Resolution {
	setting := strings.ToLower(os.Getenv(EnvVar))
	switch {
	case setting == "0" || setting == "false":
		return Resolution{Rule: RuleOptedOut}
	case isTruthy(os.Getenv(doNotTrackVar)):
		return Resolution{Rule: RuleDoNotTrack}
	case setting == debugValue:
		return Resolution{Enabled: true, Debug: true, Rule: RuleDebug}
	case key == "":
		return Resolution{Rule: RuleNoKey}
	case endpoint == "":
		return Resolution{Rule: RuleNoEndpoint}
	}
	return Resolution{Enabled: true, Rule: RuleDefault}
}

func (r Resolution) IsCollecting() bool {
	return r.Enabled && !r.Debug
}

func isTruthy(value string) bool {
	value = strings.ToLower(value)
	return value == "1" || value == "true"
}
