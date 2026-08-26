package session

import (
	"bufio"
	"context"
	"os"
	"strconv"
	"strings"
)

type Target struct {
	Alias        string
	Host         string
	Port         int
	User         string
	IdentityFile string
	Config       string
}

func (t Target) Destination() string {
	if t.Alias != "" {
		return t.Alias
	}
	return t.Host
}

func (t Target) options() []string {
	var args []string
	if t.Config != "" {
		args = append(args, "-F", t.Config)
	}
	if t.Port != 0 {
		args = append(args, "-p", strconv.Itoa(t.Port))
	}
	if t.User != "" {
		args = append(args, "-l", t.User)
	}
	if t.IdentityFile != "" {
		args = append(args, "-i", t.IdentityFile, "-o", "IdentitiesOnly=yes")
	}
	return args
}

type Destination struct {
	Written    string
	Address    string
	Port       int
	User       string
	KnownHosts []string
}

func (d Destination) Principal() string { return d.User + "@" + d.Written }

func (d Destination) entry() string {
	if d.Port == 22 {
		return d.Address
	}
	return "[" + d.Address + "]:" + strconv.Itoa(d.Port)
}

func resolve(ctx context.Context, target Target) (Destination, error) {
	rendered, err := output(ctx, "ssh", append(append([]string{"-G"}, target.options()...), target.Destination())...)
	if err != nil {
		return Destination{}, err
	}
	dest := Destination{Written: target.Destination(), Port: 22}
	scanner := bufio.NewScanner(strings.NewReader(rendered))
	for scanner.Scan() {
		key, value, split := strings.Cut(strings.TrimSpace(scanner.Text()), " ")
		if !split {
			continue
		}
		switch key {
		case "hostname":
			dest.Address = value
		case "user":
			dest.User = value
		case "port":
			if port, err := strconv.Atoi(value); err == nil {
				dest.Port = port
			}
		case "userknownhostsfile":
			files := strings.Fields(value)
			if len(files) > 0 {
				dest.KnownHosts = append(dest.KnownHosts, files[0])
				dest.KnownHosts = append(dest.KnownHosts, present(files[1:])...)
			}
		case "globalknownhostsfile":
			dest.KnownHosts = append(dest.KnownHosts, present(strings.Fields(value))...)
		}
	}
	return dest, nil
}

func present(paths []string) []string {
	var kept []string
	for _, path := range paths {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			kept = append(kept, path)
		}
	}
	return kept
}
