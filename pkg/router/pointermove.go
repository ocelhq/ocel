package router

import (
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
)

type StillActive func(ctx context.Context) error

type PointerMove struct {
	Pointer     string
	Promotion   Promotion
	Records     map[string]DeploymentRecord
	Hosts       []edge.PreviewHost
	Superseded  []edge.PreviewHost
	StillActive StillActive
}

type PointerRemoval struct {
	Pointer string             `json:"pointer"`
	Hosts   []edge.PreviewHost `json:"hosts,omitempty"`
}

func FormatDeploymentPointer(pointer, promotionID string) string {
	return ResolvePointer(pointer) + deploymentSeparator + promotionID
}

func ParseDeploymentPointer(pointer string) (preview, promotionID string, ok bool) {
	at := strings.LastIndex(pointer, deploymentSeparator)
	if at < 1 {
		return "", "", false
	}
	return pointer[:at], pointer[at+1:], true
}

const deploymentSeparator = "@"

func (m PointerMove) ListHostsToWithdraw() []edge.PreviewHost {
	return slices.DeleteFunc(slices.Clone(m.Superseded), func(host edge.PreviewHost) bool {
		return slices.Contains(m.Hosts, host)
	})
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
