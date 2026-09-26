package session

import (
	"bufio"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

var preference = []string{"ssh-ed25519", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521", "rsa-sha2-512", "rsa-sha2-256", "ssh-rsa"}

func offered(ctx context.Context, dest Destination) ([]provider.HostKey, error) {
	rendered, err := output(ctx, "ssh-keyscan", "-T", strconv.Itoa(int(reach.Seconds())), "-p", strconv.Itoa(dest.Port), dest.Address)
	if err != nil {
		return nil, err
	}
	return keysIn(rendered), nil
}

type known struct {
	keys      []provider.HostKey
	delegated bool
}

func recorded(ctx context.Context, dest Destination) known {
	var held known
	for _, file := range dest.KnownHosts {
		rendered, err := output(ctx, "ssh-keygen", "-F", dest.entry(), "-f", file)
		if err != nil {
			continue
		}
		held.keys = append(held.keys, keysIn(rendered)...)
		held.delegated = held.delegated || markedIn(rendered)
	}
	return held
}

func keysIn(rendered string) []provider.HostKey {
	var keys []provider.HostKey
	scanner := bufio.NewScanner(strings.NewReader(rendered))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || strings.HasPrefix(fields[0], "#") || strings.HasPrefix(fields[0], "@") {
			continue
		}
		if key, err := (provider.HostKey{Type: fields[1], Key: fields[2]}).Fingerprinted(); err == nil {
			keys = append(keys, key)
		}
	}
	return keys
}

const certAuthority = "@cert-authority"

func markedIn(rendered string) bool {
	scanner := bufio.NewScanner(strings.NewReader(rendered))
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) > 0 && fields[0] == certAuthority {
			return true
		}
	}
	return false
}

func classify(dest Destination, offered []provider.HostKey, held known) (provider.HostKey, *provider.HostTrust) {
	if len(offered) == 0 {
		return provider.HostKey{}, nil
	}
	ordered := preferred(offered)
	for _, key := range ordered {
		if slices.ContainsFunc(held.keys, func(k provider.HostKey) bool { return k.Key == key.Key }) {
			return key, nil
		}
	}
	if held.delegated {
		return ordered[0], nil
	}
	trust := provider.HostTrust{
		Host:       dest.Written,
		Address:    dest.Address,
		Port:       dest.Port,
		KeyAlias:   dest.KeyAlias,
		KnownHosts: dest.KnownHosts,
		Got:        ordered[0],
	}
	if len(held.keys) == 0 {
		trust.Reason = provider.UnknownHostKey
		trust.Remedy = remedy(trust)
		return provider.HostKey{}, &trust
	}
	trust.Reason = provider.HostKeyMismatch
	trust.Want = preferred(held.keys)[0]
	for _, key := range ordered {
		if paired := slices.IndexFunc(held.keys, func(k provider.HostKey) bool { return k.Type == key.Type }); paired >= 0 {
			trust.Got, trust.Want = key, held.keys[paired]
			break
		}
	}
	trust.Remedy = remedy(trust)
	return provider.HostKey{}, &trust
}

func remedy(trust provider.HostTrust) string {
	if trust.Reason == provider.HostKeyMismatch {
		return forgetting(trust.KnownHostsEntry(), trust.KnownHosts)
	}
	return fmt.Sprintf("ssh-keyscan -t %s -p %d %s >> %s", trust.Got.Type, trust.Port, trust.Address, store(trust.KnownHosts))
}

func forgetting(entry string, files []string) string {
	return fmt.Sprintf("ssh-keygen -R %s -f %s", quoted(entry), store(files))
}

func store(files []string) string {
	if len(files) == 0 {
		return "~/.ssh/known_hosts"
	}
	return quoted(files[0])
}

func quoted(word string) string {
	if plainly(word) {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

func plainly(word string) bool {
	if word == "" {
		return false
	}
	return strings.IndexFunc(word, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return false
		default:
			return !strings.ContainsRune("._-/", r)
		}
	}) < 0
}

func preferred(keys []provider.HostKey) []provider.HostKey {
	ordered := slices.Clone(keys)
	slices.SortStableFunc(ordered, func(a, b provider.HostKey) int {
		return rank(a.Type) - rank(b.Type)
	})
	return ordered
}

func rank(kind string) int {
	if at := slices.Index(preference, kind); at >= 0 {
		return at
	}
	return len(preference)
}
