package runnerclient

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"connectrpc.com/connect"

	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/tracing/content"
)

const (
	pollEvery          = 1500 * time.Millisecond
	maxPollBackoff     = 10 * time.Second
	maxUnreachableWait = 2 * time.Minute
	eventsPerPage      = 500
)

func IsJobEnded(state runnerv1.JobState) bool {
	switch state {
	case runnerv1.JobState_JOB_STATE_SUCCEEDED, runnerv1.JobState_JOB_STATE_FAILED, runnerv1.JobState_JOB_STATE_CANCELED:
		return true
	}
	return false
}

type Tail struct {
	Changed func(*runnerv1.Job)
	Logged  func(*runnerv1.JobEvent)
	Unknown func(ctx context.Context) (*runnerv1.Job, error)
}

func (c *Client) TailJob(ctx context.Context, jobID string, tail Tail) (*runnerv1.Job, error) {
	var last *runnerv1.Job
	var unreachable time.Duration
	var after uint64
	unchanged := 0
	for {
		page, err := c.listJobEvents(ctx, jobID, after)
		switch {
		case err == nil:
			unreachable = 0
			unchanged++
			for _, event := range page.GetEvents() {
				after = max(after, event.GetSeq())
				if tail.Logged != nil {
					tail.Logged(event)
				}
				unchanged = 0
			}
			job := page.GetJob()
			if last == nil || job.GetState() != last.GetState() || job.GetPhase() != last.GetPhase() || job.GetStep() != last.GetStep() || job.GetInput().GetId() != last.GetInput().GetId() {
				tail.Changed(job)
				unchanged = 0
			}
			last = job
			if IsJobEnded(job.GetState()) && !page.GetMore() {
				return job, nil
			}
			if page.GetMore() {
				continue
			}
		case ctx.Err() != nil:
			return last, ctx.Err()
		case connect.CodeOf(err) == connect.CodeNotFound && tail.Unknown != nil:
			var ended *runnerv1.Job
			ended, err = tail.Unknown(ctx)
			switch {
			case err != nil && ctx.Err() != nil:
				return last, ctx.Err()
			case err != nil && !IsTransient(err):
				return last, err
			case ended != nil:
				return ended, nil
			}
			unchanged++
		case !IsTransient(err):
			return last, fmt.Errorf("read job %s from %s at %s: %w", jobID, c.name, c.apiURL, err)
		default:
			unchanged++
		}
		wait := pollBackoff(unchanged)
		if err != nil {
			if unreachable+wait > maxUnreachableWait {
				return last, fmt.Errorf("%s could not be reached for %s, and job %s keeps running on %s: %w", c.apiURL, unreachable.Round(time.Second), jobID, c.name, err)
			}
			unreachable += wait
		}
		if err := c.sleep(ctx, wait); err != nil {
			return last, err
		}
	}
}

func (c *Client) listJobEvents(ctx context.Context, jobID string, after uint64) (*runnerv1.ListJobEventsResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	page, err := c.runner.ListJobEvents(ctx, &runnerv1.ListJobEventsRequest{JobId: jobID, AfterSeq: after, Limit: eventsPerPage})
	if err != nil {
		return nil, err
	}
	if page.GetJob() == nil {
		return nil, fmt.Errorf("no job %s in the answer", jobID)
	}
	return page, nil
}

func (c *Client) AnswerConfirmation(ctx context.Context, jobID, inputID string, confirmed bool) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	_, err := c.runner.AnswerConfirmation(ctx, &runnerv1.AnswerConfirmationRequest{JobId: jobID, InputId: inputID, Confirmed: confirmed})
	if err != nil {
		return fmt.Errorf("answer input %s of job %s on %s: %w", inputID, jobID, c.name, err)
	}
	return nil
}

func (c *Client) SupplyVariables(ctx context.Context, jobID, inputID string, values []*runnerv1.VariableValue) error {
	ctx, cancel := context.WithTimeout(content.Omit(ctx), callTimeout)
	defer cancel()
	_, err := c.runner.SupplyVariables(ctx, &runnerv1.SupplyVariablesRequest{JobId: jobID, InputId: inputID, Variables: &runnerv1.VariableValues{Values: values}})
	if err != nil {
		return fmt.Errorf("supply the values input %s of job %s needs on %s: %w", inputID, jobID, c.name, err)
	}
	return nil
}

func (c *Client) CancelJob(ctx context.Context, jobID string) error {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if _, err := c.runner.CancelJob(ctx, &runnerv1.CancelJobRequest{JobId: jobID}); err != nil {
		return fmt.Errorf("cancel job %s on %s: %w", jobID, c.name, err)
	}
	return nil
}

func IsTransient(err error) bool {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable, connect.CodeResourceExhausted, connect.CodeDeadlineExceeded, connect.CodeUnknown, connect.CodeInternal:
		return true
	}
	return false
}

func pollBackoff(unchanged int) time.Duration {
	if unchanged == 0 {
		return pollEvery
	}
	ceiling := min(pollEvery<<min(unchanged, 4), maxPollBackoff)
	return ceiling/2 + rand.N(ceiling/2)
}
