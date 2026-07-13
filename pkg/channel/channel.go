// Package channel is the private CLI<->plugin local-channel contract: the
// per-session token handshake, the Authorization header encoding, the
// readiness sentinel, and the address encoding for the Unix-socket / loopback
// TCP transports. Both the deployment provider and the resource-runtime
// binaries speak it; neither plane owns it.
package channel

import (
	"fmt"
	"strconv"
	"strings"
)

// SessionTokenEnvVar is the environment variable the CLI uses to pass a
// freshly generated per-session token to a spawned plugin process at
// launch. The plugin reads it once at startup and verifies every
// subsequent RPC call carries the same token via the Authorization header
// (see FormatAuthHeader/ParseAuthHeader) — for both the Unix-socket and
// loopback-TCP transports.
const SessionTokenEnvVar = "OCEL_SESSION_TOKEN"

// FormatAuthHeader renders a session token as the value of the RPC
// Authorization header the CLI presents on every call.
func FormatAuthHeader(token string) string {
	return "Bearer " + token
}

// ParseAuthHeader extracts the session token from an Authorization header
// value produced by FormatAuthHeader. ok is false if value isn't a
// well-formed "Bearer <token>" header, including an empty token.
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

// readinessSentinelPrefix is the exact literal prefix a plugin prints,
// once, to stdout after binding its listener and becoming ready to accept
// connections.
const readinessSentinelPrefix = "OCEL_READY"

// FormatReadinessLine renders the readiness sentinel line a plugin prints
// to stdout once bound. addr is produced by FormatUnixAddr or FormatTCPAddr.
func FormatReadinessLine(addr string) string {
	return readinessSentinelPrefix + " " + addr
}

// ParseReadinessLine extracts the address from a line of plugin stdout, if
// that line is the readiness sentinel. ok is false for any other line, which
// the caller must treat as diagnostic log output rather than protocol.
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

// FormatUnixAddr encodes a Unix domain socket path as a readiness address.
func FormatUnixAddr(path string) string {
	return "unix:" + path
}

// FormatTCPAddr encodes a loopback TCP port as a readiness address. The
// plugin must bind 127.0.0.1 only, never 0.0.0.0: loopback TCP is a
// weaker isolation posture than the Unix socket and must be revisited
// before plugins handle real cloud credentials.
func FormatTCPAddr(port int) string {
	return fmt.Sprintf("tcp:127.0.0.1:%d", port)
}

// ParseAddr decodes an address produced by FormatUnixAddr or FormatTCPAddr
// into the (network, address) pair net.Dial/net.Listen expect.
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
