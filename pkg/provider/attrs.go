package provider

import (
	"context"
	"errors"
	"strconv"
	"time"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	AttrKeyApp           = "app"
	AttrKeyResourceCount = "resource.count"
	AttrKeyBytes         = "bytes"
	AttrKeyDurationMS    = "duration.ms"
	AttrKeyResourceType  = "resource.type"
	AttrKeyResourceName  = "resource.name"
	AttrKeyErrorKind     = "error.kind"
)

func AttrApp(name string) edge.Attr {
	return edge.Attr{Key: AttrKeyApp, Value: name}
}

func AttrResourceCount(n int) edge.Attr {
	return edge.Attr{Key: AttrKeyResourceCount, Value: strconv.Itoa(n)}
}

func AttrBytes(n int64) edge.Attr {
	return edge.Attr{Key: AttrKeyBytes, Value: strconv.FormatInt(n, 10)}
}

func AttrDurationMS(d time.Duration) edge.Attr {
	return edge.Attr{Key: AttrKeyDurationMS, Value: strconv.FormatInt(d.Milliseconds(), 10)}
}

func AttrResourceType(typ string) edge.Attr {
	return edge.Attr{Key: AttrKeyResourceType, Value: typ}
}

func AttrResourceName(name string) edge.Attr {
	return edge.Attr{Key: AttrKeyResourceName, Value: name}
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
