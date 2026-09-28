package provider

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
)

const (
	AttrKeyApp            = "app"
	AttrKeyResourceCount  = "resource.count"
	AttrKeyBytes          = "bytes"
	AttrKeyDurationMS     = "duration.ms"
	AttrKeyResourceType   = "resource.type"
	AttrKeyResourceName   = "resource.name"
	AttrKeyErrorKind      = "error.kind"
	AttrKeyResourceAction = "resource.action"
)

func AttrApp(name string) progress.Attr {
	return progress.Attr{Key: AttrKeyApp, Value: name}
}

func AttrResourceCount(n int) progress.Attr {
	return progress.Attr{Key: AttrKeyResourceCount, Value: strconv.Itoa(n)}
}

func AttrBytes(n int64) progress.Attr {
	return progress.Attr{Key: AttrKeyBytes, Value: strconv.FormatInt(n, 10)}
}

func AttrDurationMS(d time.Duration) progress.Attr {
	return progress.Attr{Key: AttrKeyDurationMS, Value: strconv.FormatInt(d.Milliseconds(), 10)}
}

func AttrResourceType(typ string) progress.Attr {
	return progress.Attr{Key: AttrKeyResourceType, Value: typ}
}

func AttrResourceName(name string) progress.Attr {
	return progress.Attr{Key: AttrKeyResourceName, Value: name}
}

func AttrResourceAction(action ChangeAction) progress.Attr {
	return progress.Attr{Key: AttrKeyResourceAction, Value: string(action)}
}

const (
	ErrorKindCanceled = "canceled"
	ErrorKindTimeout  = "timeout"
	ErrorKindFailed   = "failed"
)

func ClassifyError(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return ErrorKindCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return ErrorKindTimeout
	default:
		return ErrorKindFailed
	}
}
