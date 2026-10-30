package vps

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	endpointv1 "github.com/ocelhq/ocel/pkg/proto/endpoint/v1"
	"github.com/ocelhq/ocel/pkg/proto/endpoint/v1/endpointv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	certificateKeyFile  = "key"
	certificateFile     = "key-cert.pub"
	pinnedHostKeysFile  = "known_hosts"
	exchangeTimeout     = 30 * time.Second
	firstRenewalRetry   = time.Second
	longestRenewalRetry = time.Minute
	shortestRenewal     = 10 * time.Second
)

type certificateLogin struct {
	identity provider.WebIdentity
	host     string
	port     int
	client   *http.Client
	now      func() time.Time

	mu      sync.Mutex
	dir     string
	key     string
	stop    context.CancelFunc
	renewed chan struct{}
}

func newCertificateLogin(identity provider.WebIdentity, configured string, port int) (*certificateLogin, error) {
	parsed, err := url.Parse(identity.IdentityProvider)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"the web identity names %q, which is not the https URL of a box's endpoint", identity.IdentityProvider)
	}
	if err := dialable(parsed.Hostname()); err != nil {
		return nil, err
	}
	if !strings.EqualFold(parsed.Hostname(), configured) {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"this project deploys to %s under %q, and the console's credential is for %s\nName the box under %q by the hostname the console registered", configured, "ssh", parsed.Hostname(), "ssh")
	}
	if identity.TokenFile == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid, "the web identity names no token file")
	}
	return &certificateLogin{
		identity: identity,
		host:     configured,
		port:     port,
		client:   tracing.NewClient(exchangeTimeout),
		now:      time.Now,
	}, nil
}

func (l *certificateLogin) target(ctx context.Context) (ssh.Target, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.dir == "" {
		dir, err := os.MkdirTemp("", "ocel-ssh-certificate-")
		if err != nil {
			return ssh.Target{}, fmt.Errorf("make a private directory for the ssh certificate: %w", err)
		}
		key, err := writeKey(ctx, filepath.Join(dir, certificateKeyFile))
		if err != nil {
			os.RemoveAll(dir)
			return ssh.Target{}, err
		}
		l.dir, l.key = dir, key
	}
	issued, err := l.exchange(ctx)
	if err != nil {
		return ssh.Target{}, err
	}
	if err := l.write(issued); err != nil {
		return ssh.Target{}, err
	}
	if l.stop == nil {
		renewing, stop := context.WithCancel(context.WithoutCancel(ctx))
		l.stop, l.renewed = stop, make(chan struct{})
		go l.renew(renewing, issued.GetExpiresAt().AsTime())
	}
	return ssh.Target{
		Host:            l.host,
		Port:            l.port,
		User:            issued.GetPrincipal(),
		IdentityFile:    filepath.Join(l.dir, certificateKeyFile),
		CertificateFile: filepath.Join(l.dir, certificateFile),
		KnownHostsFile:  filepath.Join(l.dir, pinnedHostKeysFile),
		Config:          ssh.NoConfig,
	}, nil
}

func writeKey(ctx context.Context, path string) (string, error) {
	cmd := tracing.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "ocel-deploy", "-f", path)
	if said, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("make the ssh key the certificate certifies: %w: %s", err, strings.TrimSpace(string(said)))
	}
	public, err := os.ReadFile(path + ".pub")
	if err != nil {
		return "", fmt.Errorf("read the ssh key the certificate certifies: %w", err)
	}
	return strings.TrimSpace(string(public)), nil
}

func (l *certificateLogin) exchange(ctx context.Context) (*endpointv1.IssueUserCertificateResponse, error) {
	token, err := os.ReadFile(l.identity.TokenFile)
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeDenied, "read the console's credential: %s", err)
	}
	ctx, cancel := context.WithTimeout(ctx, exchangeTimeout)
	defer cancel()
	client := endpointv1connect.NewSshCertificateServiceClient(&http.Client{
		Transport: bearer{token: strings.TrimSpace(string(token)), next: l.client.Transport},
		Timeout:   l.client.Timeout,
	}, strings.TrimSuffix(l.identity.IdentityProvider, "/"))
	issued, err := client.IssueUserCertificate(ctx, &endpointv1.IssueUserCertificateRequest{PublicKey: l.key})
	if err != nil {
		return nil, refusal.Refuse(refusal.CodeDenied, "%s issued no ssh certificate: %s", l.host, err)
	}
	if issued.GetPrincipal() == "" || len(issued.GetHostKeys()) == 0 {
		return nil, refusal.Refuse(refusal.CodeDenied, "%s issued a certificate naming no login or no host key", l.host)
	}
	return issued, nil
}

func (l *certificateLogin) write(issued *endpointv1.IssueUserCertificateResponse) error {
	entry := ssh.KnownHostsEntry(l.host, "", l.port)
	var pinned strings.Builder
	for _, line := range issued.GetHostKeys() {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return refusal.Refuse(refusal.CodeDenied, "%s named the host key %q, which is no key", l.host, line)
		}
		if _, err := (ssh.HostKey{Type: fields[0], Key: fields[1]}).Fingerprinted(); err != nil {
			return refusal.Refuse(refusal.CodeDenied, "%s named a host key ocel cannot read: %s", l.host, err)
		}
		pinned.WriteString(entry + " " + fields[0] + " " + fields[1] + "\n")
	}
	if err := replaceFile(filepath.Join(l.dir, pinnedHostKeysFile), []byte(pinned.String())); err != nil {
		return err
	}
	return replaceFile(filepath.Join(l.dir, certificateFile), []byte(issued.GetCertificate()+"\n"))
}

func replaceFile(path string, content []byte) error {
	staged, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	_, err = staged.Write(content)
	if closeErr := staged.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Chmod(staged.Name(), 0o600)
	}
	if err == nil {
		err = os.Rename(staged.Name(), path)
	}
	if err != nil {
		os.Remove(staged.Name())
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func (l *certificateLogin) renew(ctx context.Context, expires time.Time) {
	defer close(l.renewed)
	retry := firstRenewalRetry
	wait := renewalDelay(l.now(), expires)
	for {
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		issued, err := l.exchange(ctx)
		if err == nil {
			l.mu.Lock()
			err = l.write(issued)
			l.mu.Unlock()
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			wait = retry/2 + rand.N(retry/2+1)
			retry = min(2*retry, longestRenewalRetry)
			continue
		}
		retry = firstRenewalRetry
		wait = renewalDelay(l.now(), issued.GetExpiresAt().AsTime())
	}
}

func renewalDelay(now, expires time.Time) time.Duration {
	return max(expires.Sub(now)/2, shortestRenewal)
}

func (l *certificateLogin) close() error {
	l.mu.Lock()
	stop, renewed := l.stop, l.renewed
	l.stop = nil
	l.mu.Unlock()
	if stop != nil {
		stop()
		<-renewed
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	dir := l.dir
	l.dir, l.key = "", ""
	if dir == "" {
		return nil
	}
	return os.RemoveAll(dir)
}

type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(request *http.Request) (*http.Response, error) {
	if b.token == "" {
		return nil, errors.New("the console's credential is empty")
	}
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+b.token)
	next := b.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(request)
}
