package conformance

import (
	"regexp"
	"slices"
	"testing"

	"connectrpc.com/connect"

	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

var sourceIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func runUploads(t *testing.T, r runnerUnderTest) {
	t.Run("ListMissingBlobs names only the asked digests the organization has not uploaded", func(t *testing.T) {
		held := r.mine.upload(t, "held.ts")
		absent := digestOf(freshContent())

		if got := r.mine.missing(t, held.GetDigest(), absent); !slices.Equal(got, []string{absent}) {
			t.Errorf("ListMissingBlobs(held, absent) = %v, want only %s", got, absent)
		}
	})

	t.Run("ListMissingBlobs names another organization's blob as missing", func(t *testing.T) {
		held := r.mine.upload(t, "held.ts")

		if got := r.other.missing(t, held.GetDigest()); !slices.Equal(got, []string{held.GetDigest()}) {
			t.Errorf("ListMissingBlobs() of another organization's blob = %v, want it missing: an organization learns nothing of what another uploaded", got)
		}
	})

	t.Run("a blob whose bytes do not hash to its digest is refused or stays missing, and no source is committed from it", func(t *testing.T) {
		claimed := digestOf(freshContent())

		if status := send(t, r.mine.target(t, claimed), freshContent()); status/100 == 2 {
			if got := r.mine.missing(t, claimed); !slices.Equal(got, []string{claimed}) {
				t.Errorf("ListMissingBlobs() after its target took other bytes = %v, want %s still missing", got, claimed)
			}
		}
		_, err := r.mine.runner.CommitSource(t.Context(), &runnerv1.CommitSourceRequest{Manifest: &runnerv1.SourceManifest{Files: []*runnerv1.SourceFile{{Path: "claimed.ts", Digest: claimed, Size: 1}}}})
		if !refusedWith(err, connect.CodeFailedPrecondition) {
			t.Errorf("CommitSource() naming a blob whose bytes hash elsewhere error = %v, want %s", err, connect.CodeFailedPrecondition)
		}
	})

	t.Run("an upload target takes the blob from a caller bearing no token", func(t *testing.T) {
		body := freshContent()
		digest := digestOf(body)

		if status := send(t, r.mine.target(t, digest), body); status/100 != 2 {
			t.Fatalf("the upload target for %s answered %d to a caller bearing no token, want the blob kept: the target is its own credential", digest, status)
		}
		if got := r.mine.missing(t, digest); len(got) != 0 {
			t.Errorf("ListMissingBlobs() after its target took the blob = %v, want it held", got)
		}
	})

	t.Run("CommitSource answers the same sha256 source id for the same manifest", func(t *testing.T) {
		file := r.mine.upload(t, "index.ts")

		first, second := r.mine.commit(t, file), r.mine.commit(t, file)
		if !sourceIDPattern.MatchString(first) || first != second {
			t.Errorf("CommitSource() twice of one manifest = %q then %q, want one id of 64 lowercase hex digits, which StartJob takes", first, second)
		}
	})

	t.Run("CommitSource refuses a manifest naming a blob never uploaded", func(t *testing.T) {
		held := r.mine.upload(t, "held.ts")
		absent := &runnerv1.SourceFile{Path: "absent.ts", Digest: digestOf(freshContent()), Size: 1}

		_, err := r.mine.runner.CommitSource(t.Context(), &runnerv1.CommitSourceRequest{Manifest: &runnerv1.SourceManifest{Files: []*runnerv1.SourceFile{held, absent}}})
		if !refusedWith(err, connect.CodeFailedPrecondition) {
			t.Errorf("CommitSource() naming a blob never uploaded error = %v, want %s", err, connect.CodeFailedPrecondition)
		}
	})

	t.Run("CommitSource refuses a manifest naming another organization's blob", func(t *testing.T) {
		theirs := r.mine.upload(t, "index.ts")

		_, err := r.other.runner.CommitSource(t.Context(), &runnerv1.CommitSourceRequest{Manifest: &runnerv1.SourceManifest{Files: []*runnerv1.SourceFile{theirs}}})
		if !refusedWith(err, connect.CodeFailedPrecondition) {
			t.Errorf("CommitSource() naming another organization's blob error = %v, want %s: a source is built only from blobs its organization uploaded", err, connect.CodeFailedPrecondition)
		}
	})
}
