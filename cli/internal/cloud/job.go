package cloud

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"connectrpc.com/connect"

	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
)

const (
	pollEvery          = 1500 * time.Millisecond
	maxPollBackoff     = 15 * time.Second
	maxUnreachableWait = 2 * time.Minute
)

func (c *Client) StartJob(ctx context.Context, req *cloudv1.StartJobRequest) (*cloudv1.Job, error) {
	var started *cloudv1.StartJobResponse
	err := c.retry(ctx, func(ctx context.Context) (time.Duration, error) {
		ctx, cancel := context.WithTimeout(ctx, callTimeout)
		defer cancel()
		var err error
		started, err = c.jobs.StartJob(ctx, req)
		if connect.CodeOf(err) == connect.CodeResourceExhausted {
			return 0, retryable{err}
		}
		return 0, err
	})
	if err != nil {
		return nil, fmt.Errorf("start the job on Ocel Cloud at %s: %w", c.apiURL, err)
	}
	if started.GetJob().GetId() == "" {
		return nil, fmt.Errorf("%s started a job and named no id for it", c.apiURL)
	}
	return started.GetJob(), nil
}

func IsJobEnded(state cloudv1.JobState) bool {
	switch state {
	case cloudv1.JobState_JOB_STATE_SUCCEEDED, cloudv1.JobState_JOB_STATE_FAILED, cloudv1.JobState_JOB_STATE_CANCELED:
		return true
	}
	return false
}

func (c *Client) WaitForJob(ctx context.Context, jobID string, changed func(*cloudv1.Job)) (*cloudv1.Job, error) {
	var last *cloudv1.Job
	var unreachable time.Duration
	failures := 0
	for {
		job, err := c.getJob(ctx, jobID)
		wait := pollEvery
		switch {
		case err == nil:
			unreachable, failures = 0, 0
			if last == nil || job.GetState() != last.GetState() || job.GetPhase() != last.GetPhase() || job.GetStep() != last.GetStep() || job.GetInput().GetId() != last.GetInput().GetId() {
				changed(job)
			}
			last = job
			if IsJobEnded(job.GetState()) {
				return job, nil
			}
		case ctx.Err() != nil:
			return last, ctx.Err()
		case !isTransient(err):
			return last, fmt.Errorf("read job %s from Ocel Cloud at %s: %w", jobID, c.apiURL, err)
		default:
			failures++
			wait = pollBackoff(failures)
			if unreachable+wait > maxUnreachableWait {
				return last, fmt.Errorf("%s could not be reached for %s, and job %s keeps running on Ocel Cloud: %w", c.apiURL, unreachable.Round(time.Second), jobID, err)
			}
			unreachable += wait
		}
		if err := c.sleep(ctx, wait); err != nil {
			return last, err
		}
	}
}

func (c *Client) getJob(ctx context.Context, jobID string) (*cloudv1.Job, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	read, err := c.jobs.GetJob(ctx, &cloudv1.GetJobRequest{JobId: jobID})
	if err != nil {
		return nil, err
	}
	if read.GetJob() == nil {
		return nil, fmt.Errorf("no job %s in the answer", jobID)
	}
	return read.GetJob(), nil
}

func isTransient(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeResourceExhausted, connect.CodeDeadlineExceeded, connect.CodeUnknown, connect.CodeInternal:
		return true
	}
	return false
}

func pollBackoff(failures int) time.Duration {
	ceiling := min(pollEvery<<min(failures, 4), maxPollBackoff)
	return ceiling/2 + rand.N(ceiling/2)
}
