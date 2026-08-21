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
	"github.com/ocelhq/ocel/pkg/providerkit/resources"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Provider struct{}

var _ providerkit.Provider = Provider{}

func (Provider) Vendor() providerkit.Vendor { return "vps" }

func (Provider) Accept(context.Context, providerkit.Options) error { return nil }

// Serves is derived, not declared: the walk asks its own resource set which
// primitives it implements, so this provider cannot claim one it cannot make.
func (Provider) Serves() []providerkit.LinkType { return resources.Serves(made{}) }

func (Provider) Bootstrap() providerkit.Bootstrapper  { return bootstrapper{} }
func (Provider) Releases() providerkit.Releaser       { return resources.Releaser(made{}) }
func (Provider) Artifacts() providerkit.ArtifactStore { return artifacts{} }
func (Provider) Records() providerkit.RecordStore     { return records{} }
func (Provider) Values() providerkit.ValueStore       { return values{} }
func (Provider) Credentials() providerkit.Credentials { return credentials{} }
func (Provider) Edges() providerkit.EdgeRegistry      { return edges{} }
func (Provider) DNS() providerkit.DNSRegistry         { return zones{} }

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

// made is the per-primitive set, composed into a Releaser above. A host has no
// managed Postgres and no object store worth the name, so it implements Bucket
// (a directory), Container (a unit with an image) and Function (a unit with a
// handler) — and simply does not implement Postgres. The kit then refuses a
// manifest asking for one, by name, rather than failing halfway through a deploy.
//
// Which is also the extension point: someone who wants Postgres here embeds made,
// defines the one method against Neon, and changes nothing else.
type made struct{}

func (made) Bucket(context.Context, resources.Scope, string, providerkit.BucketSpec, providerkit.Reporter) (providerkit.Link, error) {
	return providerkit.Link{}, nil
}

func (made) Function(context.Context, resources.Scope, providerkit.FunctionSpec, []providerkit.Link, providerkit.Reporter) (providerkit.Function, error) {
	return providerkit.Function{}, nil
}

func (made) Container(context.Context, resources.Scope, string, providerkit.ContainerSpec, providerkit.Reporter) (providerkit.Link, error) {
	return providerkit.Link{}, nil
}

func (made) Remove(context.Context, resources.Scope, resources.Ref, providerkit.Reporter) error {
	return nil
}

// Neon is the override the model has to support: one method, over a base that
// knew nothing about it, with no fork of the provider and no change to the kit.
type Neon struct{ made }

func (Neon) Postgres(context.Context, resources.Scope, string, providerkit.PostgresSpec, providerkit.Reporter) (providerkit.Link, error) {
	return providerkit.Link{}, nil
}

var _ providerkit.Releaser = resources.Releaser(Neon{})

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

// records: one SQLite file, or a directory of JSON with an flock. A RecordName
// becomes a path and a Revision becomes the row version — or the file's mtime;
// on DynamoDB the same name becomes two key columns and the revision an
// attribute, and neither store had to tell the kit which it was. List returning
// whole records is free here too: a readdir is followed by a read either way.
//
// The host's edge has no ledger of its own, so it composes providerkit/ledger
// over this store and the four verbs carry the promotions.
type records struct{}

func (records) Read(context.Context, providerkit.RecordName) (providerkit.Record, error) {
	return providerkit.Record{}, nil
}

func (records) Write(context.Context, providerkit.Record) (providerkit.Revision, error) {
	return "", nil
}

func (records) Remove(context.Context, providerkit.RecordName, providerkit.Revision) error {
	return nil
}

func (records) List(context.Context, providerkit.RecordName) ([]providerkit.Record, error) {
	return nil, nil
}

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

// zones: a host writes no DNS. Supported is empty and Open answers with no writer
// and no error, which is the kit's signal to print the records owed and wait —
// the same path a Route 53 account takes when the zone lives somewhere else.
type zones struct{}

func (zones) Supported() []providerkit.DNSKind { return nil }

func (zones) Open(providerkit.DNSKind, string) (edge.DNSWriter, error) { return nil, nil }
