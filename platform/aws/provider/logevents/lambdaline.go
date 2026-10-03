package logevents

import (
	"strings"
)

var lambdaBookkeepingPrefixes = []string{"INIT_START ", "START RequestId:", "END RequestId:"}

const (
	lambdaReportPrefix = "REPORT RequestId:"
	outOfMemoryType    = "Runtime.OutOfMemory"
)

func readLambdaLine(text string) (line string, failure, keep bool) {
	for _, prefix := range lambdaBookkeepingPrefixes {
		if strings.HasPrefix(text, prefix) {
			return "", false, false
		}
	}
	if !strings.HasPrefix(text, lambdaReportPrefix) {
		return text, false, true
	}
	fields := reportFields(text)
	status, hasStatus := fields["Status"]
	switch {
	case !hasStatus:
		return "", false, false
	case status == "timeout":
		return "timed out after " + fields["Duration"], true, true
	case status == "error" && fields["Error Type"] == outOfMemoryType:
		return "ran out of memory (" + fields["Memory Size"] + ")", true, true
	}
	return text, true, true
}

func reportFields(text string) map[string]string {
	fields := map[string]string{}
	for part := range strings.SplitSeq(text, "\t") {
		if key, value, ok := strings.Cut(part, ": "); ok {
			fields[key] = strings.TrimSpace(value)
		}
	}
	return fields
}
