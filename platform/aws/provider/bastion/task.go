package bastion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

const roleNotAssumable = "unable to assume the role"

type Task struct {
	Target string

	clients Clients
	cluster string
	arn     string
	stop    sync.Once
	stopped error
}

func (b Bastion) Run(ctx context.Context, c Clients) (*Task, error) {
	arn, err := b.startTask(ctx, c)
	if err != nil {
		return nil, err
	}
	started := &Task{clients: c, cluster: b.Cluster, arn: arn}
	target, err := started.awaitExecAgent(ctx)
	if err != nil {
		return nil, errors.Join(err, started.Stop())
	}
	started.Target = target
	return started, nil
}

func (b Bastion) startTask(ctx context.Context, c Clients) (string, error) {
	input := &ecs.RunTaskInput{
		Cluster:              aws.String(b.Cluster),
		TaskDefinition:       aws.String(b.TaskDefinition),
		LaunchType:           ecstypes.LaunchTypeFargate,
		Count:                aws.Int32(1),
		EnableExecuteCommand: true,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
			Subnets:        b.Subnets,
			SecurityGroups: []string{b.SecurityGroup},
			AssignPublicIp: ecstypes.AssignPublicIpEnabled,
		}},
	}
	for attempt := 0; ; attempt++ {
		started, err := c.ECS.RunTask(ctx, input)
		refused := err
		if err == nil {
			refused = refusalOf(started.Failures)
		}
		if refused == nil && len(started.Tasks) > 0 {
			return aws.ToString(started.Tasks[0].TaskArn), nil
		}
		if refused == nil {
			refused = errors.New("ECS started no task")
		}
		if !strings.Contains(refused.Error(), roleNotAssumable) || attempt >= maxRoleAttempts {
			return "", fmt.Errorf("start a task of %s in cluster %s: %w", b.TaskDefinition, b.Cluster, refused)
		}
		if err := c.pause(ctx, attempt); err != nil {
			return "", err
		}
	}
}

const maxRoleAttempts = 12

func refusalOf(failures []ecstypes.Failure) error {
	if len(failures) == 0 {
		return nil
	}
	var reasons []string
	for _, failure := range failures {
		reasons = append(reasons, aws.ToString(failure.Reason)+" "+aws.ToString(failure.Detail))
	}
	return errors.New(strings.TrimSpace(strings.Join(reasons, "; ")))
}

func (t *Task) awaitExecAgent(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, agentReadyTimeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		described, err := t.clients.ECS.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String(t.cluster), Tasks: []string{t.arn}})
		if err != nil {
			return "", fmt.Errorf("describe task %s: %w", t.arn, err)
		}
		if len(described.Tasks) == 0 {
			return "", fmt.Errorf("task %s is gone: %w", t.arn, refusalOf(described.Failures))
		}
		current := described.Tasks[0]
		if aws.ToString(current.LastStatus) == "STOPPED" {
			return "", fmt.Errorf("task %s stopped before its ECS Exec agent ran: %s", t.arn, aws.ToString(current.StoppedReason))
		}
		if target, ready := execTarget(t.cluster, t.arn, current); ready {
			return target, nil
		}
		if err := t.clients.pause(ctx, attempt); err != nil {
			return "", fmt.Errorf("wait for the ECS Exec agent of task %s: %w", t.arn, err)
		}
	}
}

func execTarget(cluster, arn string, task ecstypes.Task) (string, bool) {
	for _, container := range task.Containers {
		if aws.ToString(container.Name) != containerName || container.RuntimeId == nil {
			continue
		}
		for _, agent := range container.ManagedAgents {
			if agent.Name == ecstypes.ManagedAgentNameExecuteCommandAgent && aws.ToString(agent.LastStatus) == "RUNNING" {
				id := arn[strings.LastIndex(arn, "/")+1:]
				return "ecs:" + cluster + "_" + id + "_" + aws.ToString(container.RuntimeId), true
			}
		}
	}
	return "", false
}

func (t *Task) Stop() error {
	t.stop.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), stopTimeout)
		defer cancel()
		_, err := t.clients.ECS.StopTask(ctx, &ecs.StopTaskInput{
			Cluster: aws.String(t.cluster),
			Task:    aws.String(t.arn),
			Reason:  aws.String("Ocel: the port forwards through this bastion ended"),
		})
		if err != nil {
			t.stopped = fmt.Errorf("stop task %s, which keeps running until it ends itself: %w", t.arn, err)
		}
	})
	return t.stopped
}
