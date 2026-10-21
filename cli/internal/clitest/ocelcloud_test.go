package clitest

import (
	"context"
	"fmt"
	"testing"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/cli/internal/version"
	"github.com/ocelhq/ocel/pkg/cloudconformance"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
)

func TestTheFakeOcelCloudAnswersAsOcelCloudDoes(t *testing.T) {
	fake := ServeOcelCloud(t)
	fake.JobReads = nil
	cloudconformance.Run(t, cloudconformance.Suite{
		URL:                    fake.APIURL,
		Token:                  FakeCloudToken,
		OtherOrganizationToken: otherOrganizationCloudToken,
		Version:                version.Version,
		Builder:                fakeBuilder{fake},
	})
}

type fakeBuilder struct{ cloud *FakeOcelCloud }

func (b fakeBuilder) Log(_ context.Context, jobID string, lines []string) error {
	b.cloud.mu.Lock()
	defer b.cloud.mu.Unlock()
	running, found := b.cloud.jobs[jobID]
	if !found {
		return fmt.Errorf("no job %s", jobID)
	}
	for _, line := range lines {
		running.events = append(running.events, &cloudv1.JobEvent{
			Seq: uint64(len(running.events) + 1), Time: timestamppb.Now(), Level: cloudv1.JobEventLevel_JOB_EVENT_LEVEL_INFO, Message: line,
		})
	}
	return nil
}

func (b fakeBuilder) Succeed(_ context.Context, jobID string) error {
	b.cloud.mu.Lock()
	defer b.cloud.mu.Unlock()
	running, found := b.cloud.jobs[jobID]
	if !found {
		return fmt.Errorf("no job %s", jobID)
	}
	running.job.State = cloudv1.JobState_JOB_STATE_SUCCEEDED
	running.job.EndedAt = timestamppb.Now()
	return nil
}
