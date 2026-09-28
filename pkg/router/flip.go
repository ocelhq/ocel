package router

import "context"

type Flip struct {
	Pointer     string
	Promotion   Promotion
	Records     map[string]DeploymentRecord
	Hostnames   []string
	StillActive func(ctx context.Context) error
}

func (f Flip) RefuseInactive(ctx context.Context) error {
	if f.StillActive == nil {
		return nil
	}
	if err := f.StillActive(ctx); err != nil {
		return Unserved{Err: err}
	}
	return nil
}
