// Package vpswalk walks a bare host through every port, so the port set is
// tested against a vendor that has no control plane at all rather than only
// against the one it was extracted from. It compiles and does nothing.
//
// The walk's verdict: every port has a plain answer on a host, and the two that
// looked cloud-shaped — artifacts and records — are the two that turn out to be
// the most portable, because a directory and a file with a lock satisfy both.
package vpswalk

import (
	"context"
	"io"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Provider struct{}

var _ providerkit.Provider = Provider{}

func (Provider) Vendor() providerkit.Vendor { return "vps" }

func (Provider) Accept(context.Context, providerkit.Options) error { return nil }

// Serves: a host's membrane can answer for whatever it can run next to the app.
// A blob store is a directory; a queue is not there unless something installs one.
func (Provider) Serves() []providerkit.LinkType { return nil }

func (Provider) Bootstrap() providerkit.Bootstrapper  { return bootstrapper{} }
func (Provider) Releases() providerkit.Releaser       { return releaser{} }
func (Provider) Artifacts() providerkit.ArtifactStore { return artifacts{} }
func (Provider) Records() providerkit.RecordStore     { return records{} }
func (Provider) Values() providerkit.ValueStore       { return values{} }
func (Provider) Credentials() providerkit.Credentials { return credentials{} }
func (Provider) Edges() providerkit.EdgeRegistry      { return edges{} }

// bootstrapper: /srv/ocel/<class>, a system user, a systemd slice, an age
// keyfile. Present is whether the directory is there; Schema is a number in a
// file the installer wrote; DigestCurrent compares a hash of the unit files this
// build carries. Nothing here needed a cloud.
type bootstrapper struct{}

func (bootstrapper) Describe(context.Context, providerkit.Class) (providerkit.Bootstrap, error) {
	return providerkit.Bootstrap{}, nil
}

func (bootstrapper) Apply(context.Context, providerkit.BootstrapRequest, providerkit.Reporter) error {
	return nil
}

func (bootstrapper) Removals(context.Context, providerkit.Class) ([]providerkit.Removal, error) {
	return nil, nil
}

func (bootstrapper) Remove(context.Context, providerkit.Class, providerkit.Reporter) error {
	return nil
}

// releaser: a release is a directory of unit files and a socket. Provision
// writes them and reloads; Destroy stops and removes them; Sweep is the same
// walk over releases the records no longer name. No Pulumi, which is the point.
type releaser struct{}

func (releaser) Provision(context.Context, providerkit.ReleasePlan, providerkit.Reporter) (providerkit.ReleaseResult, error) {
	return providerkit.ReleaseResult{}, nil
}

func (releaser) Destroy(context.Context, providerkit.ReleaseScope, providerkit.Reporter) error {
	return nil
}

func (releaser) Sweep(context.Context, providerkit.SweepScope, providerkit.Reporter) error {
	return nil
}

// Warmer and CodeEmbedder are absent, which is the test: a host has no cold
// start worth warming and swaps code by writing a directory, so the optional
// sets must be genuinely optional or this provider cannot exist.

// artifacts: a directory under the bootstrap. Bucket is the class's root.
type artifacts struct{}

func (artifacts) Put(context.Context, providerkit.ArtifactRef, io.Reader) error { return nil }

func (artifacts) Open(context.Context, providerkit.ArtifactRef) (io.ReadCloser, error) {
	return nil, nil
}

func (artifacts) RemovePrefix(context.Context, string, providerkit.Reporter) error { return nil }

// records: one SQLite file, or a directory of JSON with an flock. Swap is the
// reason it cannot be plain files: the promotion pointer needs compare-and-set,
// and a host gets that from a transaction rather than a conditional write.
type records struct{}

func (records) Get(context.Context, providerkit.RecordKey) ([]byte, error) { return nil, nil }

func (records) Put(context.Context, providerkit.RecordKey, []byte) error { return nil }

func (records) Swap(context.Context, providerkit.RecordKey, []byte, []byte) error { return nil }

func (records) Delete(context.Context, providerkit.RecordKey) error { return nil }

func (records) List(context.Context, string) ([]providerkit.RecordKey, error) { return nil, nil }

// values: age-encrypted files under a keyfile only the service user can read.
// The separation from records earns itself here — the key is the whole port.
type values struct{}

func (values) Put(context.Context, providerkit.Coordinate, []byte) (providerkit.Version, error) {
	return providerkit.Version{}, nil
}

func (values) Get(context.Context, providerkit.Coordinate) ([]byte, providerkit.Version, error) {
	return nil, providerkit.Version{}, nil
}

func (values) Delete(context.Context, providerkit.Coordinate) error { return nil }

func (values) List(context.Context, providerkit.Coordinate) ([]providerkit.Coordinate, error) {
	return nil, nil
}

func (values) Versions(context.Context, providerkit.Coordinate) ([]providerkit.Version, error) {
	return nil, nil
}

func (values) Purge(context.Context, string, providerkit.Reporter) (int, error) { return 0, nil }

// credentials: an SSH identity. Account is the host, Principal is the user,
// Problems is whether that user can write the bootstrap and reload units, and
// Policy is a sudoers fragment. The tier split survives: installing the
// bootstrap needs root, shipping a release does not.
type credentials struct{}

func (credentials) Whoami(context.Context) (providerkit.Identity, error) {
	return providerkit.Identity{Provider: "vps"}, nil
}

func (credentials) Problems(context.Context, providerkit.CredentialTier) ([]providerkit.Problem, error) {
	return nil, nil
}

func (credentials) Policy(providerkit.CredentialTier) (string, error) { return "", nil }

// edges: a host fronts itself. The registry answers with one kind and no choice,
// which is the honest shape rather than a special case.
type edges struct{}

func (edges) Supported() []edge.Kind { return []edge.Kind{"none"} }

func (edges) Default() edge.Kind { return "none" }

func (edges) Open(edge.Kind) (edge.Edge, error) { return nil, nil }
