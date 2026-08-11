package channel

import (
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
