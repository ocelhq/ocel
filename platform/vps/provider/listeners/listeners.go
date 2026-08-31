package listeners

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

const listenState = "0A"

const (
	TCPPath  = "/proc/net/tcp"
	TCP6Path = "/proc/net/tcp6"
)

type Listener struct {
	Addr netip.Addr
	Port int
}

func (l Listener) String() string { return netip.AddrPortFrom(l.Addr, uint16(l.Port)).String() }

func Parse(r io.Reader) ([]Listener, error) {
	var held []Listener
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 || !strings.HasSuffix(fields[0], ":") || fields[3] != listenState {
			continue
		}
		listener, err := local(fields[1])
		if err != nil {
			return nil, err
		}
		held = append(held, listener)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return held, nil
}

func local(field string) (Listener, error) {
	written, spelled, split := strings.Cut(field, ":")
	if !split {
		return Listener{}, fmt.Errorf("%q names no local address and port a listening socket is read from", field)
	}
	port, err := strconv.ParseUint(spelled, 16, 16)
	if err != nil {
		return Listener{}, fmt.Errorf("%q names no port: %w", field, err)
	}
	addr, err := address(written)
	if err != nil {
		return Listener{}, err
	}
	return Listener{Addr: addr, Port: int(port)}, nil
}

func address(written string) (netip.Addr, error) {
	raw, err := hex.DecodeString(written)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.Addr{}, fmt.Errorf("%q is no address a listening socket is bound to", written)
	}
	for at := 0; at < len(raw); at += 4 {
		slices.Reverse(raw[at : at+4])
	}
	addr, ok := netip.AddrFromSlice(raw)
	if !ok {
		return netip.Addr{}, fmt.Errorf("%q is no address a listening socket is bound to", written)
	}
	return addr.Unmap(), nil
}

func On(held []Listener, port int) []Listener {
	var found []Listener
	for _, listener := range held {
		if listener.Port == port {
			found = append(found, listener)
		}
	}
	return found
}

func Lines(held []Listener) []string {
	written := make([]string, 0, len(held))
	for _, listener := range held {
		written = append(written, listener.String())
	}
	return written
}

func Read(said string) ([]Listener, error) {
	var held []Listener
	for line := range strings.Lines(said) {
		spelled := strings.TrimSpace(line)
		if spelled == "" {
			continue
		}
		at, err := netip.ParseAddrPort(spelled)
		if err != nil {
			return nil, fmt.Errorf("%q is no listening socket ocel asked this host to name: %w", spelled, err)
		}
		held = append(held, Listener{Addr: at.Addr(), Port: int(at.Port())})
	}
	return held, nil
}
