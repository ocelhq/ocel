package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// bytecodeCacheCeiling bounds what the membrane will upload: an S3 PUT that
// grows unbounded with every cold start is worse than a warm start that never
// gets a cache, so the caller checks the archive against this before it ever
// reaches for a client.
const bytecodeCacheCeiling = 64 << 20 // 64 MiB

// bytecodeCacheKey composes the S3 key the membrane uploads a function's
// compile cache under and downloads it back from. prefix, functionName and
// nodeMajor are the caller's to supply — nothing here reads the environment,
// which is what keeps it callable from a test with no AWS client in sight.
func bytecodeCacheKey(prefix, functionName string, nodeMajor int, goArch string) string {
	return fmt.Sprintf("%s/bytecode/%s/node%d-%s.tar.gz", prefix, functionName, nodeMajor, s3Arch(goArch))
}

// s3Arch renders a Go GOARCH value the way AWS spells it in its own naming
// (Lambda architecture, S3 object keys): amd64 is x86_64, everything else
// (arm64 included) already matches and passes through unchanged.
func s3Arch(goArch string) string {
	if goArch == "amd64" {
		return "x86_64"
	}
	return goArch
}

var nodeMajorPattern = regexp.MustCompile(`^v?(\d+)\.`)

// nodeMajor extracts the major version number out of a Node version string
// such as "v24.3.1" or "24.3.1". The membrane cannot reliably learn the
// child's version from the flush ack, so this only ever parses a version the
// caller obtained some other way; an unparseable string returns an error
// rather than a guess.
func nodeMajor(version string) (int, error) {
	m := nodeMajorPattern.FindStringSubmatch(version)
	if m == nil {
		return 0, fmt.Errorf("not a node version: %q", version)
	}
	var major int
	if _, err := fmt.Sscanf(m[1], "%d", &major); err != nil {
		return 0, fmt.Errorf("not a node version: %q", version)
	}
	return major, nil
}

// bytecodeArchive is a gzip-compressed tar built from a compile-cache
// directory, along with the uncompressed size of the files it contains so the
// caller can weigh it against bytecodeCacheCeiling before uploading it.
type bytecodeArchive struct {
	Data             []byte
	UncompressedSize int64
}

// exceedsBytecodeCacheCeiling reports whether an archive is too large to
// upload. The ceiling is the caller's decision to act on — this only answers
// the question; it never trims the archive to fit, because a truncated
// compile cache is a corrupt one.
func exceedsBytecodeCacheCeiling(uncompressedSize int64) bool {
	return uncompressedSize > bytecodeCacheCeiling
}

// buildBytecodeArchive walks dir and tars+gzips every regular file it finds,
// preserving the paths relative to dir (Node nests its output under a
// version-hash subdirectory, so the structure carries that along). A dir that
// does not exist yields an empty archive rather than an error: no compile
// cache is not a failure the caller needs to swallow, it is simply nothing to
// upload yet.
func buildBytecodeArchive(dir string) (bytecodeArchive, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	var total int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dir {
				return nil // caller passed a dir that doesn't exist yet
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{
			Name:    filepath.ToSlash(rel),
			Size:    info.Size(),
			Mode:    int64(info.Mode().Perm()),
			ModTime: info.ModTime(),
		}); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		n, err := io.Copy(tw, f)
		if err != nil {
			return err
		}
		total += n
		return nil
	})
	if err != nil {
		return bytecodeArchive{}, fmt.Errorf("build bytecode archive from %s: %w", dir, err)
	}
	if err := tw.Close(); err != nil {
		return bytecodeArchive{}, fmt.Errorf("close bytecode archive tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return bytecodeArchive{}, fmt.Errorf("close bytecode archive gzip: %w", err)
	}
	return bytecodeArchive{Data: buf.Bytes(), UncompressedSize: total}, nil
}
