package caddyfile

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	streamCloseDelay = "30s"
	redirectToHTTPS  = "redir https://{host}{uri} 308"
	tryDuration      = 10 * time.Second
	tryInterval      = 250 * time.Millisecond
)

type shieldedSite struct {
	hostname string
	shield   proxy.Shield
}

func (c Caddyfile) upstream() string {
	if c.Network != "" {
		return net.JoinHostPort(switchboard.Name, switchboard.HTTPSListenPort)
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port))
}

func (c Caddyfile) forwarding() string {
	block := "\treverse_proxy " + c.upstream() + " {\n" +
		"\t\tlb_try_duration " + tryDuration.String() + "\n" +
		"\t\tlb_try_interval " + tryInterval.String() + "\n"
	if c.Container != "" {
		block += "\t\tstream_close_delay " + streamCloseDelay + "\n"
	}
	return block + "\t}\n"
}

func (c Caddyfile) render(spec proxy.Spec) ([]byte, error) {
	if len(spec.Hostnames) == 0 {
		return []byte("\n"), nil
	}
	open, shielded := split(spec)
	var rendered strings.Builder
	if len(open) > 0 {
		rendered.WriteString(strings.Join(open, ", ") + " {\n" + c.forwarding() + "}\n")
	}
	for _, site := range shielded {
		requiring, err := clientAuthentication(site)
		if err != nil {
			return nil, err
		}
		rendered.WriteString("https://" + site.hostname + " {\n\ttls")
		if site.shield.OriginCertificate.Certificate != "" {
			bundle := filepath.Join(c.containerDirectory(), site.shield.OriginFileName())
			rendered.WriteString(" " + bundle + " " + bundle)
		}
		rendered.WriteString(" {\n" + requiring + "\t}\n" + c.forwarding() + "}\n")
	}
	if len(shielded) > 0 {
		plain := make([]string, 0, len(shielded))
		for _, site := range shielded {
			plain = append(plain, "http://"+site.hostname)
		}
		rendered.WriteString(strings.Join(plain, ", ") + " {\n\t" + redirectToHTTPS + "\n}\n")
	}
	return []byte(rendered.String()), nil
}

func (c Caddyfile) originFiles(spec proxy.Spec) []proxy.OriginFile {
	_, shielded := split(spec)
	var files []proxy.OriginFile
	for _, site := range shielded {
		pair := site.shield.OriginCertificate
		if pair.Certificate == "" {
			continue
		}
		path := c.originPath(site.shield)
		if slices.ContainsFunc(files, func(file proxy.OriginFile) bool { return file.Path == path }) {
			continue
		}
		files = append(files, proxy.OriginFile{Path: path, Bundle: pair.Bundle()})
	}
	slices.SortFunc(files, func(a, b proxy.OriginFile) int { return strings.Compare(a.Path, b.Path) })
	return files
}

func split(spec proxy.Spec) ([]string, []shieldedSite) {
	var open []string
	var shielded []shieldedSite
	for _, hostname := range spec.Hostnames {
		if shield, found := spec.ShieldOf(hostname); found {
			shielded = append(shielded, shieldedSite{hostname: hostname, shield: shield})
			continue
		}
		open = append(open, hostname)
	}
	return open, shielded
}

func clientAuthentication(site shieldedSite) (string, error) {
	if len(site.shield.ClientCertificates) == 0 {
		return "", fmt.Errorf("%s is shielded by no client certificate", site.hostname)
	}
	requiring := "\t\tclient_auth {\n\t\t\tmode require\n"
	for _, certificate := range site.shield.ClientCertificates {
		leaf, err := encodeLeafDER(certificate)
		if err != nil {
			return "", fmt.Errorf("a client certificate %s is shielded by: %w", site.hostname, err)
		}
		requiring += "\t\t\ttrusted_leaf_cert " + leaf + "\n"
	}
	return requiring + "\t\t}\n", nil
}

func encodeLeafDER(certificate string) (string, error) {
	block, _ := pem.Decode([]byte(certificate))
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("it is no PEM certificate")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(block.Bytes), nil
}

func (c Caddyfile) originPath(shield proxy.Shield) string {
	return filepath.Join(c.Directory, shield.OriginFileName())
}

func (c Caddyfile) containerDirectory() string {
	if c.ContainerDirectory == "" {
		return c.Directory
	}
	return c.ContainerDirectory
}
