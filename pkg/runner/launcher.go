package runner

import (
	"context"
	"time"
)

const (
	TokenEnvVar   = "OCEL_JOB_TOKEN"
	ContextEnvVar = "OCEL_JOB_CONTEXT"
)

type Launch struct {
	JobID          string
	OrganizationID string
	Token          string
	Deadline       time.Time
}

type Launcher interface {
	Launch(ctx context.Context, launch Launch) (handle string, err error)
	Cancel(ctx context.Context, handle string) error
}
