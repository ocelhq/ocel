package session

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"slices"
	"strconv"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

var preference = []string{"ssh-ed25519", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521", "rsa-sha2-512", "rsa-sha2-256", "ssh-rsa"}

func offered(ctx context.Context, dest Destination) ([]providerkit.HostKey, error) {
	rendered, err := output(ctx, "ssh-keyscan", "-T", strconv.Itoa(int(reach.Seconds())), "-p", strconv.Itoa(dest.Port), dest.Address)
	if err != nil {
		return nil, err
	}
	return keysIn(rendered), nil
}

type known struct {
	keys    []providerkit.HostKey
	markers bool
}

func recorded(ctx context.Context, dest Destination) known {
	var held known
	for _, file := range dest.KnownHosts {
		rendered, err := output(ctx, "ssh-keygen", "-F", dest.entry(), "-f", file)
		if err != nil {
			continue
		}
		held.keys = append(held.keys, keysIn(rendered)...)
		held.markers = held.markers || markedIn(rendered)
	}
	return held
}

func keysIn(rendered string) []providerkit.HostKey {
	var keys []providerkit.HostKey
	scanner := bufio.NewScanner(strings.NewReader(rendered))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || strings.HasPrefix(fields[0], "#") || strings.HasPrefix(fields[0], "@") {
			continue
		}
		if key := (providerkit.HostKey{Type: fields[1], Key: fields[2]}); fingerprint(&key) {
			keys = append(keys, key)
		}
	}
	return keys
}

func markedIn(rendered string) bool {
	scanner := bufio.NewScanner(strings.NewReader(rendered))
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "@") {
			return true
		}
	}
	return false
}

func fingerprint(key *providerkit.HostKey) bool {
	blob, err := base64.StdEncoding.DecodeString(key.Key)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(blob)
	key.Fingerprint = "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
	return true
}

func classify(dest Destination, offered []providerkit.HostKey, held known) (providerkit.HostKey, *providerkit.HostTrust) {
	if len(offered) == 0 {
		return providerkit.HostKey{}, nil
	}
	ordered := preferred(offered)
	for _, key := range ordered {
		if slices.ContainsFunc(held.keys, func(k providerkit.HostKey) bool { return k.Key == key.Key }) {
			return key, nil
		}
	}
	if held.markers {
		return ordered[0], nil
	}
	trust := providerkit.HostTrust{
		Host:       dest.Written,
		Address:    dest.Address,
		Port:       dest.Port,
		KnownHosts: dest.KnownHosts,
		Got:        ordered[0],
	}
	if len(held.keys) == 0 {
		trust.Reason = providerkit.UnknownHostKey
		return providerkit.HostKey{}, &trust
	}
	trust.Reason = providerkit.HostKeyMismatch
	trust.Want = held.keys[0]
	for _, key := range ordered {
		if paired := slices.IndexFunc(held.keys, func(k providerkit.HostKey) bool { return k.Type == key.Type }); paired >= 0 {
			trust.Got, trust.Want = key, held.keys[paired]
			break
		}
	}
	return providerkit.HostKey{}, &trust
}

func preferred(keys []providerkit.HostKey) []providerkit.HostKey {
	ordered := slices.Clone(keys)
	slices.SortStableFunc(ordered, func(a, b providerkit.HostKey) int {
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
