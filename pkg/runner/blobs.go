package runner

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const BlobsPath = "/blobs/"

const maxBlobBytes = MaxSourceBytes

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type blobs struct {
	dir string
	url string
	key []byte
	now func() time.Time
}

func openBlobs(dir, publicURL string, now func() time.Time) (*blobs, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	key, err := readOrCreateKey(filepath.Join(dir, "signing-key"))
	if err != nil {
		return nil, err
	}
	return &blobs{dir: dir, url: strings.TrimRight(publicURL, "/"), key: key, now: now}, nil
}

func readOrCreateKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err == nil && len(key) == 32 {
		return key, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, writeFileAtomically(path, key, 0o600)
}

func (b *blobs) path(organization, digest string) string {
	return filepath.Join(b.dir, organizationDir(organization), digest)
}

func organizationDir(organization string) string {
	return hex.EncodeToString([]byte(organization))
}

func (b *blobs) has(organization, digest string) bool {
	info, err := os.Stat(b.path(organization, digest))
	return err == nil && info.Mode().IsRegular()
}

func (b *blobs) size(organization, digest string) (uint64, bool) {
	info, err := os.Stat(b.path(organization, digest))
	if err != nil || !info.Mode().IsRegular() {
		return 0, false
	}
	return uint64(info.Size()), true
}

func (b *blobs) touch(organization, digest string) {
	now := b.now()
	_ = os.Chtimes(b.path(organization, digest), now, now)
}

func (b *blobs) sign(method, organization, digest string, life time.Duration) string {
	expires := strconv.FormatInt(b.now().Add(life).Unix(), 10)
	query := url.Values{"expires": {expires}, "signature": {b.signature(method, organization, digest, expires)}}
	return b.url + BlobsPath + organizationDir(organization) + "/" + digest + "?" + query.Encode()
}

func (b *blobs) signature(method, organization, digest, expires string) string {
	mac := hmac.New(sha256.New, b.key)
	fmt.Fprintf(mac, "%s\n%s\n%s\n%s", method, organization, digest, expires)
	return hex.EncodeToString(mac.Sum(nil))
}

func (b *blobs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	organization, digest, err := b.admit(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodPut:
		if err := b.write(organization, digest, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		file, err := os.Open(b.path(organization, digest))
		if err != nil {
			http.Error(w, "no such blob", http.StatusNotFound)
			return
		}
		defer file.Close()
		if info, err := file.Stat(); err == nil {
			w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.Copy(w, file)
	}
}

func (b *blobs) admit(r *http.Request) (string, string, error) {
	if r.Method != http.MethodPut && r.Method != http.MethodGet {
		return "", "", fmt.Errorf("a blob takes PUT and GET, not %s", r.Method)
	}
	segments := strings.Split(strings.TrimSuffix(r.URL.Path, "/"), "/")
	if len(segments) < 2 {
		return "", "", errors.New("the URL names no blob")
	}
	encoded, digest := segments[len(segments)-2], segments[len(segments)-1]
	raw, err := hex.DecodeString(encoded)
	if err != nil || !digestPattern.MatchString(digest) {
		return "", "", errors.New("the URL names no blob")
	}
	organization := string(raw)
	query := r.URL.Query()
	expires := query.Get("expires")
	seconds, err := strconv.ParseInt(expires, 10, 64)
	if err != nil || b.now().After(time.Unix(seconds, 0)) {
		return "", "", errors.New("the signed URL has expired: ask the runner for a new one")
	}
	want := b.signature(r.Method, organization, digest, expires)
	if !hmac.Equal([]byte(want), []byte(query.Get("signature"))) {
		return "", "", errors.New("the URL's signature is not the runner's")
	}
	return organization, digest, nil
}

func (b *blobs) write(organization, digest string, body io.Reader) error {
	final := b.path(organization, digest)
	if b.has(organization, digest) {
		_, _ = io.Copy(io.Discard, io.LimitReader(body, maxBlobBytes))
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(final), ".upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temp, hash), io.LimitReader(body, maxBlobBytes+1))
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if written > maxBlobBytes {
		return errors.New("the blob holds more than 2 GiB, the most a source may hold")
	}
	if sum := hex.EncodeToString(hash.Sum(nil)); sum != digest {
		return fmt.Errorf("the bytes hash to %s, not %s", sum, digest)
	}
	return os.Rename(temp.Name(), final)
}

func writeFileAtomically(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".write-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}
