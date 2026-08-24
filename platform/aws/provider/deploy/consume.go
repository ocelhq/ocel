package deploy

import (
	"fmt"
	"strings"
)

type HandoverError struct {
	Links []string
	Stack string
}

func (e *HandoverError) Error() string {
	return fmt.Sprintf(
		"`links` binds %s, which ocel provisions in this environment today — stack %s still holds what it provisioned under %s. "+
			"Binding it hands ownership to your own infrastructure, and this deploy would delete ocel's copy: a database is torn down with no final snapshot, and its data goes with it. "+
			"Ocel hands no live resource over on its own. Back the data up, drop %s from the resource declarations and from `links`, deploy once to let ocel release it, then declare it again with the link in place",
		quoteAll(e.Links), e.Stack, thatName(len(e.Links)), quoteAll(e.Links),
	)
}

func thatName(n int) string {
	if n == 1 {
		return "that name"
	}
	return "those names"
}

func quoteAll(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, fmt.Sprintf("%q", n))
	}
	return strings.Join(quoted, ", ")
}

func describeCoordinate(class, environment string) string {
	if environment == "" {
		return class
	}
	return class + "/" + environment
}
