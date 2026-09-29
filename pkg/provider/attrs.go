package provider

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/ocelhq/ocel/pkg/progress"
)

func AttrApp(name string) progress.Attr {
	return progress.Attr{Key: progress.AttrKeyApp, Value: name}
}

func AttrResourceCount(n int) progress.Attr {
	return progress.Attr{Key: progress.AttrKeyResourceCount, Value: strconv.Itoa(n)}
}

func AttrBytes(n int64) progress.Attr {
	return progress.Attr{Key: progress.AttrKeyBytes, Value: strconv.FormatInt(n, 10)}
}

func AttrDurationMS(d time.Duration) progress.Attr {
	return progress.Attr{Key: progress.AttrKeyDurationMS, Value: strconv.FormatInt(d.Milliseconds(), 10)}
}

func AttrResourceType(typ string) progress.Attr {
	return progress.Attr{Key: progress.AttrKeyResourceType, Value: typ}
}

func AttrResourceName(name string) progress.Attr {
	return progress.Attr{Key: progress.AttrKeyResourceName, Value: name}
}

func AttrResourceAction(action ChangeAction) progress.Attr {
	return progress.Attr{Key: progress.AttrKeyResourceAction, Value: string(action)}
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
