package resources

import (
	"errors"
	"slices"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

var errResourceOperationFailed = errors.New("resource operation failed")

var changeSpanNames = map[provider.ChangeAction]string{
	provider.ActionCreate: "create resource",
	provider.ActionDelete: "delete resource",
}

func reportChange(log progress.Log, kind, name string, action provider.ChangeAction, start time.Time, err error) {
	if log == nil || (action == provider.ActionKeep && err == nil) {
		return
	}
	attrs := []progress.Attr{provider.AttrDurationMS(time.Since(start)), provider.AttrResourceType(kind), provider.AttrResourceName(name)}
	if action != provider.ActionKeep {
		attrs = append(attrs, provider.AttrResourceAction(action))
	}
	if err != nil {
		log.Span(errResourceOperationFailed.Error(), start, time.Now(), errResourceOperationFailed, attrs...)
		return
	}
	log.Span(changeSpanNames[action], start, time.Now(), nil, attrs...)
}

func reportCreatedCompute(log progress.Log, recorded stackrecords.Stack, provisioned provider.StackResult, start time.Time) {
	for _, function := range provisioned.Functions {
		held := slices.ContainsFunc(recorded.Functions, functionNamed(function.Name))
		reportChange(log, functionKind, function.Name, provider.KeepOrCreate(held), start, nil)
	}
	for _, container := range provisioned.Containers {
		held := slices.ContainsFunc(recorded.Containers, containerNamed(container.Name))
		reportChange(log, containerKind, container.Name, provider.KeepOrCreate(held), start, nil)
	}
}
