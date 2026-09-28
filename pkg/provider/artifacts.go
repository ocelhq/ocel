package provider

import (
	"context"
	"io"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
)

type ArtifactStore interface {
	Put(ctx context.Context, ref ArtifactRef, body io.Reader) error

	Has(ctx context.Context, ref ArtifactRef) (bool, error)

	Open(ctx context.Context, ref ArtifactRef) (io.ReadCloser, error)

	RemovePrefix(ctx context.Context, tier environment.Tier, prefix string, progress progress.Progress) error
}

type ArtifactRef struct {
	Tier   environment.Tier `json:"tier,omitempty"`
	Bucket string           `json:"bucket,omitempty"`
	Key    string           `json:"key,omitempty"`
}

const (
	StoreFunctions = "functions"
	StoreAssets    = "assets"
	StoreCache     = "cache"
)

type Upload struct {
	Name   string
	Ref    ArtifactRef
	Path   string
	Digest string
}

const UploadKind = "artifact"

type PackAppRequest struct {
	Ref    StackRef
	Edge   edge.Kind
	App    string
	Values AppValues
}

type PackAppResult struct {
	Overlay map[string][]byte

	VendorState any
}
