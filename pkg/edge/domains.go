package edge

import (
	"slices"
)

func (s StackState) BoundTo(hostname string) bool {
	return slices.Contains(s.Bound, hostname)
}

func (s *StackState) Bind(hostname string) {
	if hostname == "" || slices.Contains(s.Bound, hostname) {
		return
	}
	bound := append(slices.Clone(s.Bound), hostname)
	slices.Sort(bound)
	s.Bound = bound
}

func (s *StackState) Release(hostname string) {
	bound := slices.DeleteFunc(slices.Clone(s.Bound), func(host string) bool {
		return host == hostname
	})
	if len(bound) == 0 {
		bound = nil
	}
	s.Bound = bound
}
