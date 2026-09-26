package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const OverrideEnvVar = "OCEL_PROVIDERS_DIR"

const TokenEnvVar = "GITHUB_TOKEN"

const DefaultBaseURL = "https://github.com/ocelhq/ocel/releases/download"

const ChecksumsAsset = "checksums.txt"

const installedDigestFile = ".executable.sha256"

var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

const (
	attempts       = 5
	firstBackoff   = 500 * time.Millisecond
	maxBackoff     = 8 * time.Second
	archiveCeiling = 512 << 20
)

type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

type Store struct {
	Dir      string
	Override string
	Version  string
	Platform Platform
	BaseURL  string
	Token    string
	HTTP     Doer
	Sleep    func(time.Duration)
	Verify   ChecksumVerifier
}

func DefaultDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find the user cache directory the providers live in: %w", err)
	}
	return filepath.Join(cache, "ocel", "providers"), nil
}

func New(version string) (*Store, error) {
	dir, err := DefaultDir()
	if err != nil {
		return nil, err
	}
	return &Store{
		Dir:      dir,
		Override: os.Getenv(OverrideEnvVar),
		Version:  version,
		Platform: Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
		BaseURL:  DefaultBaseURL,
		Token:    os.Getenv(TokenEnvVar),
		HTTP:     &http.Client{Timeout: 10 * time.Minute},
		Sleep:    time.Sleep,
		Verify:   VerifyChecksums,
	}, nil
}

func (s *Store) Fetches() bool { return s.Override == "" }

func (s *Store) Binary(ctx context.Context, kind Kind, name string, platform Platform, digest string) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	executable := ExecutableName(kind, name, platform.GOOS)

	if s.Override != "" {
		path := filepath.Join(s.Override, string(kind), name, s.Version, platform.Dir(), executable)
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("%s is %s, which contains no %s %s %s for %s — build the binaries this CLI's own version needs, or unset %s to fetch them",
				OverrideEnvVar, s.Override, name, kind, s.Version, platform.Dir(), OverrideEnvVar)
		}
		return path, nil
	}

	if digest == "" {
		return "", fmt.Errorf("no lock pins the %s %s %s for %s", name, kind, s.Version, platform.Dir())
	}
	if !hexDigest.MatchString(digest) {
		return "", fmt.Errorf("the lock pins %q for the %s %s %s for %s, which is not a sha256", digest, name, kind, s.Version, platform.Dir())
	}

	dir := filepath.Join(s.Dir, string(kind), name, s.Version, platform.Dir(), digest)
	path := filepath.Join(dir, executable)

	if err := installed(dir, executable); err == nil {
		return path, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		if err := os.RemoveAll(dir); err != nil {
			return "", fmt.Errorf("clear %s out of the provider cache: %w", dir, err)
		}
	}

	if err := s.install(ctx, kind, name, platform, digest, dir, executable); err != nil {
		return "", err
	}
	return path, nil
}

func installed(dir, executable string) error {
	recorded, err := os.ReadFile(filepath.Join(dir, installedDigestFile))
	if err != nil {
		return err
	}
	got, err := hashFile(filepath.Join(dir, executable))
	if err != nil {
		return err
	}
	if want := strings.TrimSpace(string(recorded)); got != want {
		return fmt.Errorf("the cached %s hashes to %s and its install recorded %s", executable, got, want)
	}
	return nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func (s *Store) Checksums(ctx context.Context) (map[string]string, error) {
	checksums, err := s.read(ctx, ChecksumsAsset)
	if err != nil {
		return nil, err
	}
	signature, err := s.read(ctx, SignatureAsset)
	if err != nil {
		return nil, fmt.Errorf("read the signature release %s publishes over %s: %w", s.Version, ChecksumsAsset, err)
	}

	verify := s.Verify
	if verify == nil {
		verify = VerifyChecksums
	}
	if err := verify(checksums, signature, SignerIdentity(s.Version)); err != nil {
		return nil, err
	}
	return ParseChecksums(bytes.NewReader(checksums))
}

func (s *Store) read(ctx context.Context, asset string) ([]byte, error) {
	body, err := s.get(ctx, asset)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	var downloaded bytes.Buffer
	if err := fill(&downloaded, body); err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	return downloaded.Bytes(), nil
}

func (s *Store) install(ctx context.Context, kind Kind, name string, platform Platform, digest, dir, executable string) error {
	asset := AssetName(kind, name, s.Version, platform.GOOS, platform.GOARCH)
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("make the provider cache at %s: %w", s.Dir, err)
	}
	staged, err := os.MkdirTemp(s.Dir, ".fetch-")
	if err != nil {
		return fmt.Errorf("open a directory to fetch %s into: %w", asset, err)
	}
	defer os.RemoveAll(staged)

	archive := filepath.Join(staged, "archive")
	if err := s.download(ctx, asset, archive, digest); err != nil {
		return err
	}

	unpacked := filepath.Join(staged, "unpacked")
	if err := unpack(archive, platform.GOOS, unpacked); err != nil {
		return fmt.Errorf("unpack %s: %w", asset, err)
	}
	sum, err := hashFile(filepath.Join(unpacked, executable))
	if err != nil {
		return fmt.Errorf("%s contains no %s", asset, executable)
	}
	record, err := os.OpenFile(filepath.Join(unpacked, installedDigestFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("record what %s unpacked to: %w", asset, err)
	}
	if _, err := io.WriteString(record, sum+"\n"); err != nil {
		record.Close()
		return fmt.Errorf("record what %s unpacked to: %w", asset, err)
	}
	if err := record.Close(); err != nil {
		return fmt.Errorf("record what %s unpacked to: %w", asset, err)
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return fmt.Errorf("make room in the provider cache: %w", err)
	}
	if err := os.Rename(unpacked, dir); err != nil {
		if raced := installed(dir, executable); raced == nil {
			return nil
		}
		return fmt.Errorf("move %s into the provider cache: %w", asset, err)
	}
	return nil
}

func (s *Store) download(ctx context.Context, asset, into, digest string) error {
	body, err := s.get(ctx, asset)
	if err != nil {
		return err
	}
	defer body.Close()

	file, err := os.Create(into)
	if err != nil {
		return fmt.Errorf("open a file to download %s into: %w", asset, err)
	}
	defer file.Close()

	sum := sha256.New()
	if err := fill(io.MultiWriter(file, sum), body); err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != digest {
		return fmt.Errorf("%s hashes to %s and the lock pins %s; nothing was written to the provider cache", asset, got, digest)
	}
	return nil
}

type statusError struct {
	status int
	asset  string
	after  string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("the release answered %d for %s", e.status, e.asset)
}

func (s *Store) get(ctx context.Context, asset string) (io.ReadCloser, error) {
	url := s.BaseURL + "/v" + s.Version + "/" + asset

	var last error
	for attempt := range attempts {
		if attempt > 0 {
			s.Sleep(backoff(attempt, retryAfter(last)))
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("reach %s: %w", url, err)
		}
		if s.Token != "" {
			req.Header.Set("Authorization", "Bearer "+s.Token)
		}

		resp, err := s.HTTP.Do(req)
		if err != nil {
			last = fmt.Errorf("fetch %s: %w", asset, err)
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return resp.Body, nil
		}

		refused := &statusError{status: resp.StatusCode, asset: asset, after: resp.Header.Get("Retry-After")}
		resp.Body.Close()
		if !worthRetrying(resp.StatusCode) {
			return nil, refused
		}
		last = refused
	}
	return nil, fmt.Errorf("gave up after %d attempts: %w", attempts, last)
}

func retryAfter(err error) time.Duration {
	var refused *statusError
	if !errors.As(err, &refused) || refused.after == "" {
		return 0
	}
	seconds, parseErr := strconv.Atoi(refused.after)
	if parseErr != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func worthRetrying(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func backoff(attempt int, asked time.Duration) time.Duration {
	ceiling := min(firstBackoff<<(attempt-1), maxBackoff)
	if asked > ceiling {
		ceiling = min(asked, maxBackoff)
	}
	return ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2)))
}
