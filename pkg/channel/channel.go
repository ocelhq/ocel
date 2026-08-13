package channel

import (
	"context"
	"crypto/subtle"
	"fmt"
	"strconv"
	"strings"
)

const SessionTokenEnvVar = "OCEL_SESSION_TOKEN"

func FormatAuthHeader(token string) string {
	return "Bearer " + token
}

func ParseAuthHeader(value string) (token string, ok bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(value, prefix) {
		return "", false
	}
	token = value[len(prefix):]
	if token == "" {
		return "", false
	}
	return token, true
}

func VerifyAuthHeader(value, token string) bool {
	got, ok := ParseAuthHeader(value)
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}

const readinessSentinelPrefix = "OCEL_READY"

func FormatReadinessLine(addr string) string {
	return readinessSentinelPrefix + " " + addr
}

func ParseReadinessLine(line string) (addr string, ok bool) {
	prefix := readinessSentinelPrefix + " "
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	addr = line[len(prefix):]
	if addr == "" {
		return "", false
	}
	return addr, true
}

func FormatUnixAddr(path string) string {
	return "unix:" + path
}

func FormatTCPAddr(port int) string {
	return fmt.Sprintf("tcp:127.0.0.1:%d", port)
}

const TraceParentHeader = "traceparent"

type traceParentKey struct{}

func WithTraceParent(ctx context.Context, traceparent string) context.Context {
	if !ValidTraceParent(traceparent) {
		return ctx
	}
	return context.WithValue(ctx, traceParentKey{}, traceparent)
}

func TraceParentFromContext(ctx context.Context) (string, bool) {
	traceparent, ok := ctx.Value(traceParentKey{}).(string)
	return traceparent, ok
}

// ValidTraceParent reports whether value is a well-formed W3C traceparent
// header: version-traceid-parentid-flags, each a fixed-width lowercase hex
// field, with neither the trace id nor the parent id all zeros. This is the
// one place that parses the header, so both the CLI's outgoing interceptor
// (via WithTraceParent) and the provider's incoming one reject a malformed
// value the same way rather than propagating it.
func ValidTraceParent(value string) bool {
	fields := strings.Split(value, "-")
	if len(fields) != 4 {
		return false
	}
	version, traceID, parentID, flags := fields[0], fields[1], fields[2], fields[3]
	if !isLowerHex(version, 2) || !isLowerHex(traceID, 32) || !isLowerHex(parentID, 16) || !isLowerHex(flags, 2) {
		return false
	}
	if version == "ff" {
		return false
	}
	if isAllZero(traceID) || isAllZero(parentID) {
		return false
	}
	return true
}

func isLowerHex(s string, length int) bool {
	if len(s) != length {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

func isAllZero(s string) bool {
	for _, r := range s {
		if r != '0' {
			return false
		}
	}
	return true
}

func ParseAddr(addr string) (network, address string, err error) {
	switch {
	case strings.HasPrefix(addr, "unix:"):
		address = strings.TrimPrefix(addr, "unix:")
		if address == "" {
			return "", "", fmt.Errorf("channel: empty unix socket path in addr %q", addr)
		}
		return "unix", address, nil
	case strings.HasPrefix(addr, "tcp:"):
		address = strings.TrimPrefix(addr, "tcp:")
		host, port, found := strings.Cut(address, ":")
		if !found || host == "" || port == "" {
			return "", "", fmt.Errorf("channel: malformed tcp addr %q", addr)
		}
		if _, err := strconv.Atoi(port); err != nil {
			return "", "", fmt.Errorf("channel: malformed tcp port in addr %q: %w", addr, err)
		}
		return "tcp", address, nil
	default:
		return "", "", fmt.Errorf("channel: unknown address scheme in %q", addr)
	}
}
