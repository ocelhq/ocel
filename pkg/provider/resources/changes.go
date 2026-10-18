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
	provider.ActionCreate:  "create resource",
	provider.ActionUpdate:  "update resource",
	provider.ActionReplace: "replace resource",
	provider.ActionDelete:  "delete resource",
}

func ReportChange(log progress.Log, kind, name string, action provider.ChangeAction, start time.Time, err error) {
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

func reportProvisionedCompute(log progress.Log, recorded stackrecords.Stack, provisioned provider.StackResult, start time.Time) {
	for _, function := range provisioned.Functions {
		ReportChange(log, functionKind, function.Name, classifyFunctionChange(recorded.Functions, function), start, nil)
	}
	for _, container := range provisioned.Containers {
		ReportChange(log, containerKind, container.Name, classifyContainerChange(recorded.Containers, container), start, nil)
	}
}

func classifyFunctionChange(recorded []provider.Function, provisioned provider.Function) provider.ChangeAction {
	i := slices.IndexFunc(recorded, functionNamed(provisioned.Name))
	switch {
	case i < 0:
		return provider.ActionCreate
	case recorded[i].Physical != provisioned.Physical || recorded[i].Revision != provisioned.Revision:
		return provider.ActionUpdate
	default:
		return provider.ActionKeep
	}
}

func classifyContainerChange(recorded []provider.AppContainer, provisioned provider.AppContainer) provider.ChangeAction {
	i := slices.IndexFunc(recorded, containerNamed(provisioned.Name))
	switch {
	case i < 0:
		return provider.ActionCreate
	case recorded[i].Physical != provisioned.Physical || recorded[i].Image != provisioned.Image || recorded[i].Revision != provisioned.Revision:
		return provider.ActionUpdate
	default:
		return provider.ActionKeep
	}
}
