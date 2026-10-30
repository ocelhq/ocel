package runnerv1_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"

	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

var digest = strings.Repeat("ab", 32)

func validManifest() *runnerv1.SourceManifest {
	return &runnerv1.SourceManifest{Files: []*runnerv1.SourceFile{
		{Path: "bin/run", Digest: digest, Size: 12, Executable: true},
		{Path: "current", SymlinkTarget: "apps/web"},
		{Path: "package.json", Digest: digest, Size: 0},
	}}
}

func validCommitSourceRequest() *runnerv1.CommitSourceRequest {
	return &runnerv1.CommitSourceRequest{Manifest: validManifest()}
}

func validListMissingBlobsRequest() *runnerv1.ListMissingBlobsRequest {
	return &runnerv1.ListMissingBlobsRequest{Digests: []string{digest}}
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
	requireAccepted(t, "no digests", &runnerv1.ListMissingBlobsRequest{})
	requireAccepted(t, "one digest", validListMissingBlobsRequest())
	requireAccepted(t, "a manifest of files and symlinks", validCommitSourceRequest())

	full := &runnerv1.ListMissingBlobsRequest{}
	for i := range 20000 {
		full.Digests = append(full.Digests, fmt.Sprintf("%064x", i))
	}
	requireAccepted(t, "20000 digests", full)

	large := &runnerv1.CommitSourceRequest{Manifest: &runnerv1.SourceManifest{}}
	for i := range 200000 {
		large.Manifest.Files = append(large.Manifest.Files, &runnerv1.SourceFile{Path: fmt.Sprintf("src/%d.ts", i), Digest: digest})
	}
	requireAccepted(t, "200000 files", large)
}

func TestProtovalidateRefusesAListMissingBlobsRequestWithAMalformedDigest(t *testing.T) {
	requireRefusals(t, validListMissingBlobsRequest, []refusal[*runnerv1.ListMissingBlobsRequest]{
		{name: "a digest is 64 characters", mutate: func(r *runnerv1.ListMissingBlobsRequest) { r.Digests = []string{"abcd"} }},
		{name: "a digest is lowercase hex", mutate: func(r *runnerv1.ListMissingBlobsRequest) { r.Digests = []string{strings.ToUpper(digest)} }},
		{name: "each digest is named once", mutate: func(r *runnerv1.ListMissingBlobsRequest) { r.Digests = []string{digest, digest} }},
		{name: "at most 20000 digests", mutate: func(r *runnerv1.ListMissingBlobsRequest) {
			r.Digests = nil
			for i := range 20001 {
				r.Digests = append(r.Digests, fmt.Sprintf("%064x", i))
			}
		}},
	})
}

func TestProtovalidateRefusesACommitThatBreaksAManifestRule(t *testing.T) {
	requireRefusals(t, validCommitSourceRequest, []refusal[*runnerv1.CommitSourceRequest]{
		{name: "the manifest is required", mutate: func(r *runnerv1.CommitSourceRequest) { r.Manifest = nil }},
		{name: "the manifest lists a file", mutate: func(r *runnerv1.CommitSourceRequest) { r.Manifest.Files = nil }},
		{
			name: "each path is listed once",
			mutate: func(r *runnerv1.CommitSourceRequest) {
				r.Manifest.Files[1] = proto.Clone(r.Manifest.Files[0]).(*runnerv1.SourceFile)
			},
			rule: "source_manifest.unique_paths",
		},
		{name: "a path is required", mutate: func(r *runnerv1.CommitSourceRequest) { r.Manifest.Files[0].Path = "" }},
		{name: "a path is relative", mutate: func(r *runnerv1.CommitSourceRequest) { r.Manifest.Files[0].Path = "/etc/passwd" }, rule: "source_file.path"},
		{name: "a path does not climb", mutate: func(r *runnerv1.CommitSourceRequest) { r.Manifest.Files[0].Path = "../secrets" }, rule: "source_file.path"},
		{name: "a path has no dot segment", mutate: func(r *runnerv1.CommitSourceRequest) { r.Manifest.Files[0].Path = "./bin/run" }, rule: "source_file.path"},
		{name: "a path has no empty segment", mutate: func(r *runnerv1.CommitSourceRequest) { r.Manifest.Files[0].Path = "bin//run" }, rule: "source_file.path"},
		{name: "a path has no trailing slash", mutate: func(r *runnerv1.CommitSourceRequest) { r.Manifest.Files[0].Path = "bin/" }, rule: "source_file.path"},
		{name: "a path is slash-separated", mutate: func(r *runnerv1.CommitSourceRequest) { r.Manifest.Files[0].Path = `bin\run` }, rule: "source_file.path"},
		{name: "a path has no control character", mutate: func(r *runnerv1.CommitSourceRequest) { r.Manifest.Files[0].Path = "bin/r\nun" }, rule: "source_file.path"},
		{name: "a digest is lowercase hex", mutate: func(r *runnerv1.CommitSourceRequest) { r.Manifest.Files[0].Digest = "nope" }},
		{
			name:   "a file has a digest or a symlink target",
			mutate: func(r *runnerv1.CommitSourceRequest) { r.Manifest.Files[0].Digest = "" },
			rule:   "source_file.digest_or_symlink",
		},
		{
			name:   "a file is not both a regular file and a symlink",
			mutate: func(r *runnerv1.CommitSourceRequest) { r.Manifest.Files[1].Digest = digest },
			rule:   "source_file.digest_or_symlink",
		},
		{
			name:   "a symlink is not executable",
			mutate: func(r *runnerv1.CommitSourceRequest) { r.Manifest.Files[1].Executable = true },
			rule:   "source_file.symlink_is_bare",
		},
		{
			name:   "a symlink has no size",
			mutate: func(r *runnerv1.CommitSourceRequest) { r.Manifest.Files[1].Size = 3 },
			rule:   "source_file.symlink_is_bare",
		},
		{
			name:   "a symlink target is relative",
			mutate: func(r *runnerv1.CommitSourceRequest) { r.Manifest.Files[1].SymlinkTarget = "/etc" },
			rule:   "source_file.symlink_target",
		},
	})
}

func validListMissingBlobsResponse() *runnerv1.ListMissingBlobsResponse {
	return &runnerv1.ListMissingBlobsResponse{Uploads: []*runnerv1.Upload{
		{Digest: digest, Url: "https://storage.example/bucket/" + digest + "?signature=abc", Method: "PUT", Headers: map[string]string{"Content-Type": "application/octet-stream"}},
		{Digest: strings.Repeat("cd", 32), Url: "https://storage.example/bucket", Method: "POST", Fields: map[string]string{"key": "org/" + digest, "policy": "e30="}},
	}}
}

func TestProtovalidateAcceptsEveryUploadTargetARunnerCanHandOut(t *testing.T) {
	requireAccepted(t, "nothing missing", &runnerv1.ListMissingBlobsResponse{})
	requireAccepted(t, "a PUT and a POST policy", validListMissingBlobsResponse())
	requireAccepted(t, "a target served over http", &runnerv1.ListMissingBlobsResponse{Uploads: []*runnerv1.Upload{{Digest: digest, Url: "http://127.0.0.1:9000/" + digest, Method: "PUT"}}})

	full := &runnerv1.ListMissingBlobsResponse{}
	for i := range 20000 {
		full.Uploads = append(full.Uploads, &runnerv1.Upload{Digest: fmt.Sprintf("%064x", i), Url: "https://storage.example/b", Method: "PUT"})
	}
	requireAccepted(t, "20000 targets", full)
}

func TestProtovalidateRefusesAnUploadTargetThatBreaksAnUploadRule(t *testing.T) {
	requireRefusals(t, validListMissingBlobsResponse, []refusal[*runnerv1.ListMissingBlobsResponse]{
		{name: "a digest is 64 characters", mutate: func(r *runnerv1.ListMissingBlobsResponse) { r.Uploads[0].Digest = "abcd" }},
		{name: "a digest is lowercase hex", mutate: func(r *runnerv1.ListMissingBlobsResponse) { r.Uploads[0].Digest = strings.ToUpper(digest) }},
		{
			name:   "each digest is named once",
			mutate: func(r *runnerv1.ListMissingBlobsResponse) { r.Uploads[1].Digest = digest },
			rule:   "list_missing_blobs_response.unique_digests",
		},
		{name: "a url is required", mutate: func(r *runnerv1.ListMissingBlobsResponse) { r.Uploads[0].Url = "" }},
		{name: "a url is absolute", mutate: func(r *runnerv1.ListMissingBlobsResponse) { r.Uploads[0].Url = "/bucket/" + digest }},
		{name: "a url is https or http", mutate: func(r *runnerv1.ListMissingBlobsResponse) { r.Uploads[0].Url = "file:///etc/passwd" }, rule: "upload.url_scheme"},
		{name: "a method is required", mutate: func(r *runnerv1.ListMissingBlobsResponse) { r.Uploads[0].Method = "" }},
		{name: "a method is PUT or POST", mutate: func(r *runnerv1.ListMissingBlobsResponse) { r.Uploads[0].Method = "PATCH" }},
		{name: "a method is uppercase", mutate: func(r *runnerv1.ListMissingBlobsResponse) { r.Uploads[0].Method = "put" }},
		{
			name:   "only a POST policy has fields",
			mutate: func(r *runnerv1.ListMissingBlobsResponse) { r.Uploads[0].Fields = map[string]string{"key": "x"} },
			rule:   "upload.fields_only_for_post",
		},
		{name: "at most 20000 targets", mutate: func(r *runnerv1.ListMissingBlobsResponse) {
			r.Uploads = nil
			for i := range 20001 {
				r.Uploads = append(r.Uploads, &runnerv1.Upload{Digest: fmt.Sprintf("%064x", i), Url: "https://storage.example/b", Method: "PUT"})
			}
		}},
	})
}
