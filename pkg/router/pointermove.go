package router

import "context"

type StillActive func(ctx context.Context) error

type PointerMove struct {
	Pointer     string
	Promotion   Promotion
	Records     map[string]DeploymentRecord
	Hostnames   []string
	StillActive StillActive
}

func (m PointerMove) RefuseInactive(ctx context.Context) error {
	if m.StillActive == nil {
		return nil
	}
	if err := m.StillActive(ctx); err != nil {
		return Unserved{Err: err}
	}
	return nil
}
