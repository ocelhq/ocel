package scattered

import "context"

func (s *Scattered) Forget(_ context.Context, hostnames []string) ([]string, error) {
	return hostnames, nil
}
