package logevents

import (
	"strings"
)

var lambdaBookkeepingPrefixes = []string{"INIT_START ", "START RequestId:", "END RequestId:"}

const (
	lambdaReportPrefix     = "REPORT RequestId:"
	lambdaInitReportPrefix = "INIT_REPORT "
	outOfMemoryErrorType   = "Runtime.OutOfMemory"
)

func parseLambdaLine(message string) (Event, bool) {
	for _, prefix := range lambdaBookkeepingPrefixes {
		if strings.HasPrefix(message, prefix) {
			return Event{}, false
		}
	}
	switch {
	case strings.HasPrefix(message, lambdaReportPrefix):
		return parseReport(message)
	case strings.HasPrefix(message, lambdaInitReportPrefix):
		return parseInitReport(message)
	}
	return Event{Text: message}, true
}

func parseReport(message string) (Event, bool) {
	fields := parseReportFields(message)
	status, hasStatus := fields["Status"]
	switch {
	case !hasStatus:
		return Event{}, false
	case status == "timeout":
		return Event{Text: "timed out after " + fields["Duration"], Failure: true}, true
	case status == "error" && fields["Error Type"] == outOfMemoryErrorType:
		return Event{Text: "ran out of memory (" + fields["Memory Size"] + ")", Failure: true}, true
	}
	return Event{Text: message, Failure: true}, true
}

func parseInitReport(message string) (Event, bool) {
	fields := parseReportFields(strings.TrimPrefix(message, lambdaInitReportPrefix))
	status, hasStatus := fields["Status"]
	switch {
	case !hasStatus:
		return Event{}, false
	case status == "timeout":
		return Event{Text: "timed out after " + fields["Init Duration"], Failure: true}, true
	}
	return Event{Text: message, Failure: true}, true
}

func parseReportFields(message string) map[string]string {
	fields := map[string]string{}
	for part := range strings.SplitSeq(message, "\t") {
		if key, value, ok := strings.Cut(part, ": "); ok {
			fields[key] = strings.TrimSpace(value)
		}
	}
	return fields
}
