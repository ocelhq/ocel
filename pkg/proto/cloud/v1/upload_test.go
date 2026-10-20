package cloudv1_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"

	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
)

var digest = strings.Repeat("ab", 32)

func validManifest() *cloudv1.SourceManifest {
	return &cloudv1.SourceManifest{Files: []*cloudv1.SourceFile{
		{Path: "bin/run", Digest: digest, Size: 12, Executable: true},
		{Path: "current", SymlinkTarget: "apps/web"},
		{Path: "package.json", Digest: digest, Size: 0},
	}}
}

func validCommitSourceRequest() *cloudv1.CommitSourceRequest {
	return &cloudv1.CommitSourceRequest{Manifest: validManifest()}
}

func validMissingRequest() *cloudv1.MissingRequest {
	return &cloudv1.MissingRequest{Digests: []string{digest}}
}

type refusal[T proto.Message] struct {
	name   string
	mutate func(T)
	rule   string
}

func requireRefusals[T proto.Message](t *testing.T, valid func() T, cases []refusal[T]) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := valid()
			tc.mutate(msg)
			err := protovalidate.Validate(msg)
			if err == nil {
				t.Fatal("expected protovalidate to refuse it")
			}
			if tc.rule == "" {
				return
			}
			var violations *protovalidate.ValidationError
			if !errors.As(err, &violations) {
				t.Fatalf("expected a validation error, got %v", err)
			}
			for _, v := range violations.Violations {
				if v.Proto.GetRuleId() == tc.rule {
					return
				}
			}
			t.Fatalf("expected rule %s among %v", tc.rule, err)
		})
	}
}

func requireAccepted(t *testing.T, name string, msg proto.Message) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if err := protovalidate.Validate(msg); err != nil {
			t.Fatalf("expected protovalidate to accept it, got %v", err)
		}
	})
}

func TestProtovalidateAcceptsEveryUploadRequestTheCLICanSend(t *testing.T) {
	requireAccepted(t, "no digests", &cloudv1.MissingRequest{})
	requireAccepted(t, "one digest", validMissingRequest())
	requireAccepted(t, "a manifest of files and symlinks", validCommitSourceRequest())

	full := &cloudv1.MissingRequest{}
	for i := range 20000 {
		full.Digests = append(full.Digests, fmt.Sprintf("%064x", i))
	}
	requireAccepted(t, "20000 digests", full)

	large := &cloudv1.CommitSourceRequest{Manifest: &cloudv1.SourceManifest{}}
	for i := range 200000 {
		large.Manifest.Files = append(large.Manifest.Files, &cloudv1.SourceFile{Path: fmt.Sprintf("src/%d.ts", i), Digest: digest})
	}
	requireAccepted(t, "200000 files", large)
}

func TestProtovalidateRefusesAMissingRequestWithAMalformedDigest(t *testing.T) {
	requireRefusals(t, validMissingRequest, []refusal[*cloudv1.MissingRequest]{
		{name: "a digest is 64 characters", mutate: func(r *cloudv1.MissingRequest) { r.Digests = []string{"abcd"} }},
		{name: "a digest is lowercase hex", mutate: func(r *cloudv1.MissingRequest) { r.Digests = []string{strings.ToUpper(digest)} }},
		{name: "each digest is named once", mutate: func(r *cloudv1.MissingRequest) { r.Digests = []string{digest, digest} }},
		{name: "at most 20000 digests", mutate: func(r *cloudv1.MissingRequest) {
			r.Digests = nil
			for i := range 20001 {
				r.Digests = append(r.Digests, fmt.Sprintf("%064x", i))
			}
		}},
	})
}

func TestProtovalidateRefusesACommitThatBreaksAManifestRule(t *testing.T) {
	requireRefusals(t, validCommitSourceRequest, []refusal[*cloudv1.CommitSourceRequest]{
		{name: "the manifest is required", mutate: func(r *cloudv1.CommitSourceRequest) { r.Manifest = nil }},
		{name: "the manifest lists a file", mutate: func(r *cloudv1.CommitSourceRequest) { r.Manifest.Files = nil }},
		{
			name: "each path is listed once",
			mutate: func(r *cloudv1.CommitSourceRequest) {
				r.Manifest.Files[1] = proto.Clone(r.Manifest.Files[0]).(*cloudv1.SourceFile)
			},
			rule: "source_manifest.unique_paths",
		},
		{name: "a path is required", mutate: func(r *cloudv1.CommitSourceRequest) { r.Manifest.Files[0].Path = "" }},
		{name: "a path is relative", mutate: func(r *cloudv1.CommitSourceRequest) { r.Manifest.Files[0].Path = "/etc/passwd" }, rule: "source_file.path"},
		{name: "a path does not climb", mutate: func(r *cloudv1.CommitSourceRequest) { r.Manifest.Files[0].Path = "../secrets" }, rule: "source_file.path"},
		{name: "a path has no dot segment", mutate: func(r *cloudv1.CommitSourceRequest) { r.Manifest.Files[0].Path = "./bin/run" }, rule: "source_file.path"},
		{name: "a path has no empty segment", mutate: func(r *cloudv1.CommitSourceRequest) { r.Manifest.Files[0].Path = "bin//run" }, rule: "source_file.path"},
		{name: "a path has no trailing slash", mutate: func(r *cloudv1.CommitSourceRequest) { r.Manifest.Files[0].Path = "bin/" }, rule: "source_file.path"},
		{name: "a path is slash-separated", mutate: func(r *cloudv1.CommitSourceRequest) { r.Manifest.Files[0].Path = `bin\run` }, rule: "source_file.path"},
		{name: "a path has no control character", mutate: func(r *cloudv1.CommitSourceRequest) { r.Manifest.Files[0].Path = "bin/r\nun" }, rule: "source_file.path"},
		{name: "a digest is lowercase hex", mutate: func(r *cloudv1.CommitSourceRequest) { r.Manifest.Files[0].Digest = "nope" }},
		{
			name:   "a file has a digest or a symlink target",
			mutate: func(r *cloudv1.CommitSourceRequest) { r.Manifest.Files[0].Digest = "" },
			rule:   "source_file.digest_or_symlink",
		},
		{
			name:   "a file is not both a regular file and a symlink",
			mutate: func(r *cloudv1.CommitSourceRequest) { r.Manifest.Files[1].Digest = digest },
			rule:   "source_file.digest_or_symlink",
		},
		{
			name:   "a symlink is not executable",
			mutate: func(r *cloudv1.CommitSourceRequest) { r.Manifest.Files[1].Executable = true },
			rule:   "source_file.symlink_is_bare",
		},
		{
			name:   "a symlink has no size",
			mutate: func(r *cloudv1.CommitSourceRequest) { r.Manifest.Files[1].Size = 3 },
			rule:   "source_file.symlink_is_bare",
		},
		{
			name:   "a symlink target is relative",
			mutate: func(r *cloudv1.CommitSourceRequest) { r.Manifest.Files[1].SymlinkTarget = "/etc" },
			rule:   "source_file.symlink_target",
		},
	})
}
