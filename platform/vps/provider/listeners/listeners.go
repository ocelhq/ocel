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

const (
	SocketsMark = "#sockets"
	NamesMark   = "#names"
)

type Listener struct {
	Addr    netip.Addr
	Port    int
	Inode   uint64
	Holders []string
}

func (l Listener) String() string { return netip.AddrPortFrom(l.Addr, uint16(l.Port)).String() }

func Parse(r io.Reader) ([]Listener, error) {
	var held []Listener
	sockets := map[uint64][]string{}
	names := map[string]string{}
	section := ""
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if mark := strings.TrimSpace(line); mark == SocketsMark || mark == NamesMark {
			section = mark
			continue
		}
		switch section {
		case SocketsMark:
			pid, inode, err := socket(line)
			if err != nil {
				return nil, err
			}
			sockets[inode] = append(sockets[inode], pid)
		case NamesMark:
			pid, name, err := named(line)
			if err != nil {
				return nil, err
			}
			names[pid] = name
		default:
			listener, listening, err := tabled(line)
			if err != nil {
				return nil, err
			}
			if listening {
				held = append(held, listener)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	for at := range held {
		held[at].Holders = holding(sockets[held[at].Inode], names)
	}
	return held, nil
}

func tabled(line string) (Listener, bool, error) {
	fields := strings.Fields(line)
	if len(fields) < 4 || !strings.HasSuffix(fields[0], ":") || fields[3] != listenState {
		return Listener{}, false, nil
	}
	listener, err := local(fields[1])
	if err != nil {
		return Listener{}, false, err
	}
	if len(fields) > 9 {
		if listener.Inode, err = strconv.ParseUint(fields[9], 10, 64); err != nil {
			return Listener{}, false, fmt.Errorf("%q names no socket inode: %w", fields[9], err)
		}
	}
	return listener, true, nil
}

func socket(line string) (string, uint64, error) {
	dir, link, split := strings.Cut(strings.TrimSpace(line), " ")
	pid, fd, pidded := strings.Cut(strings.TrimPrefix(dir, "/proc/"), "/")
	spelled, closed := strings.CutSuffix(strings.TrimPrefix(link, "socket:["), "]")
	if !split || !pidded || fd != "fd" || !numbered(pid) || !closed || !strings.HasPrefix(link, "socket:[") {
		return "", 0, fmt.Errorf("%q is not a process's socket", line)
	}
	inode, err := strconv.ParseUint(spelled, 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("%q names no socket inode: %w", line, err)
	}
	return pid, inode, nil
}

func named(line string) (string, string, error) {
	path, name, split := strings.Cut(line, ":")
	pid, comm, pidded := strings.Cut(strings.TrimPrefix(path, "/proc/"), "/")
	if !split || !pidded || comm != "comm" || !numbered(pid) {
		return "", "", fmt.Errorf("%q is not a process's name", line)
	}
	return pid, name, nil
}

func numbered(said string) bool {
	_, err := strconv.ParseUint(said, 10, 64)
	return err == nil
}

func holding(pids []string, names map[string]string) []string {
	var held []string
	for _, pid := range pids {
		if name := names[pid]; name != "" {
			held = append(held, name)
		}
	}
	slices.Sort(held)
	return slices.Compact(held)
}

func Holders(held []Listener) []string {
	var named []string
	for _, listener := range held {
		named = append(named, listener.Holders...)
	}
	slices.Sort(named)
	return slices.Compact(named)
}

func local(field string) (Listener, error) {
	written, spelled, split := strings.Cut(field, ":")
	if !split {
		return Listener{}, fmt.Errorf("%q has no local address and port", field)
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
		return netip.Addr{}, fmt.Errorf("%q is not a socket address", written)
	}
	for at := 0; at < len(raw); at += 4 {
		slices.Reverse(raw[at : at+4])
	}
	addr, ok := netip.AddrFromSlice(raw)
	if !ok {
		return netip.Addr{}, fmt.Errorf("%q is not a socket address", written)
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
			return nil, fmt.Errorf("%q is not a listening socket: %w", spelled, err)
		}
		held = append(held, Listener{Addr: at.Addr(), Port: int(at.Port())})
	}
	return held, nil
}
