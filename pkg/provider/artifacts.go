package provider

import (
	"context"
	"io"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type ArtifactStore interface {
	Put(ctx context.Context, ref ArtifactRef, body io.Reader) error

	Has(ctx context.Context, ref ArtifactRef) (bool, error)

	Open(ctx context.Context, ref ArtifactRef) (io.ReadCloser, error)

	RemovePrefix(ctx context.Context, class edge.Class, prefix string, progress edge.Progress) error
}

type ArtifactRef struct {
	Class  edge.Class `json:"class,omitempty"`
	Bucket string     `json:"bucket,omitempty"`
	Key    string     `json:"key,omitempty"`
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
