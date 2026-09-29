package localrpc

import (
	"context"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
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

func FormatReadinessLine(version, address string, certDER []byte) string {
	return readinessSentinelPrefix + " " + version + " " + address + " " + base64.StdEncoding.EncodeToString(certDER)
}

type Readiness struct {
	Version string
	Address string
	Cert    *x509.Certificate
}

func ParseReadinessLine(line string) (Readiness, bool) {
	prefix := readinessSentinelPrefix + " "
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, prefix) {
		return Readiness{}, false
	}
	version, rest, found := strings.Cut(line[len(prefix):], " ")
	if !found || version == "" {
		return Readiness{}, false
	}
	split := strings.LastIndex(rest, " ")
	if split <= 0 {
		return Readiness{}, false
	}
	address, encoded := rest[:split], rest[split+1:]
	if address == "" || encoded == "" {
		return Readiness{}, false
	}
	der, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return Readiness{}, false
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return Readiness{}, false
	}
	return Readiness{Version: version, Address: address, Cert: cert}, true
}

func FormatUnixAddress(path string) string {
	return "unix:" + path
}

func FormatTCPAddress(port int) string {
	return fmt.Sprintf("tcp:127.0.0.1:%d", port)
}

const TraceParentHeader = "traceparent"

type traceParentKey struct{}

func WithTraceParent(ctx context.Context, traceparent string) context.Context {
	if !IsValidTraceParent(traceparent) {
		return ctx
	}
	return context.WithValue(ctx, traceParentKey{}, traceparent)
}

func TraceParentFromContext(ctx context.Context) (string, bool) {
	traceparent, ok := ctx.Value(traceParentKey{}).(string)
	return traceparent, ok
}

func IsValidTraceParent(value string) bool {
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

func ParseAddress(formatted string) (network, address string, err error) {
	switch {
	case strings.HasPrefix(formatted, "unix:"):
		address = strings.TrimPrefix(formatted, "unix:")
		if address == "" {
			return "", "", fmt.Errorf("localrpc: empty unix socket path in address %q", formatted)
		}
		return "unix", address, nil
	case strings.HasPrefix(formatted, "tcp:"):
		address = strings.TrimPrefix(formatted, "tcp:")
		host, port, found := strings.Cut(address, ":")
		if !found || host == "" || port == "" {
			return "", "", fmt.Errorf("localrpc: malformed tcp address %q", formatted)
		}
		if _, err := strconv.Atoi(port); err != nil {
			return "", "", fmt.Errorf("localrpc: malformed tcp port in address %q: %w", formatted, err)
		}
		return "tcp", address, nil
	default:
		return "", "", fmt.Errorf("localrpc: unknown address scheme in %q", formatted)
	}
}
