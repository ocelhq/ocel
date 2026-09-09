package providers

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func ParseChecksums(r io.Reader) (map[string]string, error) {
	sums := map[string]string{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		digest, asset, found := strings.Cut(strings.TrimSpace(scanner.Text()), " ")
		if !found {
			continue
		}
		asset = strings.TrimPrefix(strings.TrimSpace(asset), "*")
		if digest == "" || asset == "" {
			continue
		}
		sums[asset] = digest
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read the release checksums: %w", err)
	}
	if len(sums) == 0 {
		return nil, errors.New("the release checksums list nothing")
	}
	return sums, nil
}

func unpack(archive, goos, into string) error {
	if err := os.MkdirAll(into, 0o755); err != nil {
		return err
	}
	if goos == "windows" {
		return unzip(archive, into)
	}
	return untar(archive, into)
}

func untar(archive, into string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()

	zipped, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer zipped.Close()

	reader := tar.NewReader(zipped)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		path, err := member(into, header.Name)
		if err != nil {
			return err
		}
		if err := spill(path, reader, os.FileMode(header.Mode).Perm()); err != nil {
			return err
		}
	}
}

func unzip(archive, into string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		path, err := member(into, entry.Name)
		if err != nil {
			return err
		}
		body, err := entry.Open()
		if err != nil {
			return err
		}
		err = spill(path, body, entry.Mode().Perm())
		body.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func member(into, name string) (string, error) {
	path := filepath.Join(into, filepath.FromSlash(name))
	if !strings.HasPrefix(path, filepath.Clean(into)+string(os.PathSeparator)) {
		return "", fmt.Errorf("the archive names %q, which lands outside the directory it unpacks into", name)
	}
	return path, nil
}

func spill(path string, body io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if mode == 0 {
		mode = 0o644
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, io.LimitReader(body, archiveCeling)); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
