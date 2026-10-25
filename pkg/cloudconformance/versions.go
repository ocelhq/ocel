package cloudconformance

import (
	"testing"

	"connectrpc.com/connect"

	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
)

func runVersions(t *testing.T, c cloud) {
	for name, version := range map[string]string{
		"naming no ocel version":                          "",
		"naming an ocel version the cloud does not serve": unservedVersion,
	} {
		t.Run("Missing, CommitSource and StartJob refuse a call "+name+", saying which versions are served", func(t *testing.T) {
			refusesTheCallersVersion(t, c, newCaller(c.suite.URL, c.suite.Token, version))
		})
	}

	t.Run("GetJob, ListJobEvents, RespondJob and CancelJob answer a call naming no ocel version", func(t *testing.T) {
		job := c.startRunning(t)

		if _, err := c.console.jobs.GetJob(t.Context(), &cloudv1.GetJobRequest{JobId: job.GetId()}); err != nil {
			t.Errorf("GetJob() naming no version error = %v, want the job", err)
		}
		if _, err := c.console.jobs.ListJobEvents(t.Context(), &cloudv1.ListJobEventsRequest{JobId: job.GetId()}); err != nil {
			t.Errorf("ListJobEvents() naming no version error = %v, want the job's events", err)
		}
		if _, err := c.console.jobs.RespondJob(t.Context(), confirm(job.GetId())); versionUnserved(err) != nil {
			t.Errorf("RespondJob() naming no version error = %v, want no version refused", err)
		}
		if _, err := c.console.jobs.CancelJob(t.Context(), &cloudv1.CancelJobRequest{JobId: job.GetId()}); err != nil {
			t.Errorf("CancelJob() naming no version error = %v, want the job canceled", err)
		}
	})
}

func refusesTheCallersVersion(t *testing.T, c cloud, unserved caller) {
	t.Helper()

	file := c.cli.upload(t, "index.ts")
	source := c.cli.commit(t, file)

	_, err := unserved.uploads.Missing(t.Context(), &cloudv1.MissingRequest{Digests: []string{file.GetDigest()}})
	refusesTheVersion(t, "Missing()", err, c.suite.Version)

	_, err = unserved.uploads.CommitSource(t.Context(), &cloudv1.CommitSourceRequest{Manifest: &cloudv1.SourceManifest{Files: []*cloudv1.SourceFile{file}}})
	refusesTheVersion(t, "CommitSource()", err, c.suite.Version)

	started, err := unserved.jobs.StartJob(t.Context(), buildRequest(source))
	c.console.cancelWhenTheTestEnds(t, started.GetJob().GetId())
	refusesTheVersion(t, "StartJob()", err, c.suite.Version)
}

func refusesACallBearingNoToken(t *testing.T, c cloud) {
	anonymous := newCaller(c.suite.URL, "", c.suite.Version)

	_, err := anonymous.uploads.Missing(t.Context(), &cloudv1.MissingRequest{Digests: []string{digestOf(freshContent())}})
	if !refusedWith(err, connect.CodeUnauthenticated) {
		t.Errorf("Missing() bearing no token error = %v, want %s", err, connect.CodeUnauthenticated)
	}
	_, err = anonymous.jobs.GetJob(t.Context(), &cloudv1.GetJobRequest{JobId: "job_never_started"})
	if !refusedWith(err, connect.CodeUnauthenticated) {
		t.Errorf("GetJob() bearing no token error = %v, want %s", err, connect.CodeUnauthenticated)
	}
}
