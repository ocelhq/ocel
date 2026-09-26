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
	Addr   netip.Addr
	Port   int
	Inode  uint64
	Owners []string
}

func (l Listener) String() string { return netip.AddrPortFrom(l.Addr, uint16(l.Port)).String() }

func Parse(r io.Reader) ([]Listener, error) {
	var found []Listener
	sockets := map[uint64][]string{}
	names := map[string]string{}
	sections := []string{"", SocketsMark, NamesMark}
	section, naming := 0, ""
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if next := slices.Index(sections, strings.TrimSpace(line)); next > section {
			section = next
			continue
		}
		switch sections[section] {
		case SocketsMark:
			if pid, inode, read := socketLine(line); read {
				sockets[inode] = append(sockets[inode], pid)
			}
		case NamesMark:
			pid, name, read := nameLine(line)
			switch {
			case !read:
				naming = ""
			case pid == naming:
				names[pid] += `\n` + name
			default:
				names[pid], naming = name, pid
			}
		default:
			listener, listening, err := tableRow(line)
			if err != nil {
				return nil, err
			}
			if listening {
				found = append(found, listener)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	for at := range found {
		found[at].Owners = namesOf(sockets[found[at].Inode], names)
	}
	return found, nil
}

func tableRow(line string) (Listener, bool, error) {
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

func socketLine(line string) (string, uint64, bool) {
	dir, link, split := strings.Cut(strings.TrimSpace(line), " ")
	pid, fd, pidded := strings.Cut(strings.TrimPrefix(dir, "/proc/"), "/")
	spelled, linked := strings.CutPrefix(link, "socket:[")
	spelled, closed := strings.CutSuffix(spelled, "]")
	if !split || !pidded || fd != "fd" || !numbered(pid) || !linked || !closed {
		return "", 0, false
	}
	inode, err := strconv.ParseUint(spelled, 10, 64)
	return pid, inode, err == nil
}

func nameLine(line string) (string, string, bool) {
	path, name, split := strings.Cut(line, ":")
	pid, comm, pidded := strings.Cut(strings.TrimPrefix(path, "/proc/"), "/")
	return pid, name, split && pidded && comm == "comm" && numbered(pid)
}

func numbered(said string) bool {
	_, err := strconv.ParseUint(said, 10, 64)
	return err == nil
}

func namesOf(pids []string, names map[string]string) []string {
	var owners []string
	for _, pid := range pids {
		if name := names[pid]; name != "" {
			owners = append(owners, name)
		}
	}
	slices.Sort(owners)
	return slices.Compact(owners)
}

func Owners(listening []Listener) []string {
	var owners []string
	for _, listener := range listening {
		owners = append(owners, listener.Owners...)
	}
	slices.Sort(owners)
	return slices.Compact(owners)
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

func On(listening []Listener, port int) []Listener {
	var found []Listener
	for _, listener := range listening {
		if listener.Port == port {
			found = append(found, listener)
		}
	}
	return found
}

func Lines(listening []Listener) []string {
	written := make([]string, 0, len(listening))
	for _, listener := range listening {
		written = append(written, listener.String())
	}
	return written
}

func Read(said string) ([]Listener, error) {
	var found []Listener
	for line := range strings.Lines(said) {
		spelled := strings.TrimSpace(line)
		if spelled == "" {
			continue
		}
		at, err := netip.ParseAddrPort(spelled)
		if err != nil {
			return nil, fmt.Errorf("%q is not a listening socket: %w", spelled, err)
		}
		found = append(found, Listener{Addr: at.Addr(), Port: int(at.Port())})
	}
	return found, nil
}
