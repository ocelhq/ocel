package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

const OverrideEnvVar = "OCEL_PROVIDERS_DIR"

const TokenEnvVar = "GITHUB_TOKEN"

const DefaultBaseURL = "https://github.com/ocelhq/ocel/releases/download"

const ChecksumsAsset = "checksums.txt"

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
	}, nil
}

func (s *Store) Fetches() bool { return s.Override == "" }

func (s *Store) Binary(ctx context.Context, name, digest string) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	executable := ExecutableName(name, s.Platform.GOOS)

	if s.Override != "" {
		path := filepath.Join(s.Override, name, s.Version, s.Platform.Dir(), executable)
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("%s is %s, which holds no %s provider %s for %s — build the providers this CLI's own version needs, or unset %s to fetch them",
				OverrideEnvVar, s.Override, name, s.Version, s.Platform.Dir(), OverrideEnvVar)
		}
		return path, nil
	}

	dir := filepath.Join(s.Dir, name, s.Version, s.Platform.Dir())
	path := filepath.Join(dir, executable)
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	if err := s.install(ctx, name, digest, dir, executable); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Store) Checksums(ctx context.Context) (map[string]string, error) {
	body, err := s.get(ctx, ChecksumsAsset)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return ParseChecksums(body)
}

func (s *Store) install(ctx context.Context, name, digest, dir, executable string) error {
	if digest == "" {
		return fmt.Errorf("no lock pins the %s provider %s for %s", name, s.Version, s.Platform.Dir())
	}

	asset := AssetName(name, s.Version, s.Platform.GOOS, s.Platform.GOARCH)
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
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
	if err := unpack(archive, s.Platform.GOOS, unpacked); err != nil {
		return fmt.Errorf("unpack %s: %w", asset, err)
	}
	if _, err := os.Stat(filepath.Join(unpacked, executable)); err != nil {
		return fmt.Errorf("%s holds no %s", asset, executable)
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return fmt.Errorf("make room in the provider cache: %w", err)
	}
	if err := os.Rename(unpacked, dir); err != nil {
		if _, raced := os.Stat(filepath.Join(dir, executable)); raced == nil {
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
