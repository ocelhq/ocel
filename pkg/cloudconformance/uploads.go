package cloudconformance

import (
	"regexp"
	"slices"
	"testing"

	"connectrpc.com/connect"

	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
)

var sourceIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func runUploads(t *testing.T, c cloud) {
	t.Run("Missing names only the asked digests the organization has not uploaded", func(t *testing.T) {
		held := c.cli.upload(t, "held.ts")
		absent := digestOf(freshContent())

		if got := c.cli.missing(t, held.GetDigest(), absent); !slices.Equal(got, []string{absent}) {
			t.Errorf("Missing(held, absent) = %v, want only %s", got, absent)
		}
	})

	t.Run("Missing names another organization's blob as missing", func(t *testing.T) {
		held := c.cli.upload(t, "held.ts")

		if got := c.otherCLI.missing(t, held.GetDigest()); !slices.Equal(got, []string{held.GetDigest()}) {
			t.Errorf("Missing() of another organization's blob = %v, want it missing: an organization learns nothing of what another uploaded", got)
		}
	})

	t.Run("a blob whose bytes do not hash to its digest is refused and stays missing", func(t *testing.T) {
		claimed := digestOf(freshContent())

		if status := c.cli.put(t, claimed, freshContent()); status/100 == 2 {
			t.Errorf("PUT /v1/blobs/%s of other bytes answered %d, want it refused", claimed, status)
		}
		if got := c.cli.missing(t, claimed); !slices.Equal(got, []string{claimed}) {
			t.Errorf("Missing() after a refused PUT = %v, want %s still missing", got, claimed)
		}
	})

	t.Run("CommitSource answers the same sha256 source id for the same manifest", func(t *testing.T) {
		file := c.cli.upload(t, "index.ts")

		first, second := c.cli.commit(t, file), c.cli.commit(t, file)
		if !sourceIDPattern.MatchString(first) || first != second {
			t.Errorf("CommitSource() twice of one manifest = %q then %q, want one id of 64 lowercase hex digits, which StartJob takes", first, second)
		}
	})

	t.Run("CommitSource refuses a manifest naming a blob never uploaded", func(t *testing.T) {
		held := c.cli.upload(t, "held.ts")
		absent := &cloudv1.SourceFile{Path: "absent.ts", Digest: digestOf(freshContent()), Size: 1}

		_, err := c.cli.uploads.CommitSource(t.Context(), &cloudv1.CommitSourceRequest{Manifest: &cloudv1.SourceManifest{Files: []*cloudv1.SourceFile{held, absent}}})
		if !refusedWith(err, connect.CodeFailedPrecondition) {
			t.Errorf("CommitSource() naming a blob never uploaded error = %v, want %s", err, connect.CodeFailedPrecondition)
		}
	})

	t.Run("CommitSource refuses a manifest naming another organization's blob", func(t *testing.T) {
		theirs := c.cli.upload(t, "index.ts")

		_, err := c.otherCLI.uploads.CommitSource(t.Context(), &cloudv1.CommitSourceRequest{Manifest: &cloudv1.SourceManifest{Files: []*cloudv1.SourceFile{theirs}}})
		if !refusedWith(err, connect.CodeFailedPrecondition) {
			t.Errorf("CommitSource() naming another organization's blob error = %v, want %s: a source is built only from blobs its organization uploaded", err, connect.CodeFailedPrecondition)
		}
	})
}
