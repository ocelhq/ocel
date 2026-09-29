package deploy

import (
	"os"
	"strings"
)

const PRNumberEnvVar = "OCEL_PR_NUMBER"

func DiscoverPRNumber() string {
	if n := os.Getenv(PRNumberEnvVar); n != "" {
		return n
	}
	return prNumberFromRef(os.Getenv("GITHUB_REF"))
}

func prNumberFromRef(ref string) string {
	rest, ok := strings.CutPrefix(ref, "refs/pull/")
	if !ok {
		return ""
	}
	number, suffix, ok := strings.Cut(rest, "/")
	if !ok || number == "" || (suffix != "merge" && suffix != "head") {
		return ""
	}
	for _, r := range number {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return number
}
