package edge

import (
	"maps"
	"slices"
	"strings"
)

const StackKeyDomains = "domains"

func BoundDomains(state StackState) []string {
	raw := state[StackKeyDomains]
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

func RecordBoundDomain(state StackState, hostname string) StackState {
	hosts := BoundDomains(state)
	if hostname != "" && !slices.Contains(hosts, hostname) {
		hosts = append(hosts, hostname)
	}
	return withBoundDomains(state, hosts)
}

func ForgetBoundDomain(state StackState, hostname string) StackState {
	return withBoundDomains(state, slices.DeleteFunc(BoundDomains(state), func(host string) bool {
		return host == hostname
	}))
}

func withBoundDomains(state StackState, hosts []string) StackState {
	next := maps.Clone(state)
	if next == nil {
		next = StackState{}
	}
	if len(hosts) == 0 {
		delete(next, StackKeyDomains)
		return next
	}
	slices.Sort(hosts)
	next[StackKeyDomains] = strings.Join(hosts, ",")
	return next
}
