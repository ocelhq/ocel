package proxy

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

type Proxy interface {
	Guarantees() Guarantees
	Render(admission Admission) ([]byte, error)
	Unrendered(config []byte) string
	Reload(ctx context.Context) error
	Inspect(ctx context.Context) (Standing, error)
	Certificate(ctx context.Context, hostname string) (Certificate, error)
	Forget(ctx context.Context, hostnames []string) ([]string, error)
}

type Guarantees struct {
	OwnsPorts              bool
	IssuesCertificates     bool
	ForgetsCertificates    bool
	HonoursPins            bool
	ReportsRateLimits      bool
	ServesPreviewWildcards bool
}

type Admission struct {
	Entries     []Entry
	PreviewBase string
	Upstream    string
	Edge        string
}

type Entry struct {
	Hostname string
	Pin      string
}

type Standing []providerkit.StandingCheck

type Certificate struct {
	Renewal string
	Trouble error
}
