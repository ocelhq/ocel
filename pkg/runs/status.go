package runs

import (
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

var wireStatuses = map[provider.RunStatus]taskv1.RunStatus{
	provider.RunDelayed:   taskv1.RunStatus_RUN_STATUS_DELAYED,
	provider.RunQueued:    taskv1.RunStatus_RUN_STATUS_QUEUED,
	provider.RunExecuting: taskv1.RunStatus_RUN_STATUS_EXECUTING,
	provider.RunCompleted: taskv1.RunStatus_RUN_STATUS_COMPLETED,
	provider.RunFailed:    taskv1.RunStatus_RUN_STATUS_FAILED,
	provider.RunCanceled:  taskv1.RunStatus_RUN_STATUS_CANCELED,
	provider.RunExpired:   taskv1.RunStatus_RUN_STATUS_EXPIRED,
	provider.RunTimedOut:  taskv1.RunStatus_RUN_STATUS_TIMED_OUT,
}

func IsWaiting(status provider.RunStatus) bool {
	return status == provider.RunDelayed || status == provider.RunQueued
}

func IsUnfinished(status provider.RunStatus) bool {
	return IsWaiting(status) || status == provider.RunExecuting
}

func StatusesOf(wire []taskv1.RunStatus) []provider.RunStatus {
	var stored []provider.RunStatus
	for _, status := range wire {
		for run, answered := range wireStatuses {
			if answered == status {
				stored = append(stored, run)
			}
		}
	}
	return stored
}
