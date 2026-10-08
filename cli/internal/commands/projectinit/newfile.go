package projectinit

import (
	"errors"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
)

type newFile struct {
	write func(*os.File, []byte) error
	link  func(oldname, newname string) error
}

var systemNewFile = newFile{write: writeAll, link: os.Link}

func writeAll(file *os.File, content []byte) error {
	_, err := file.Write(content)
	return err
}

func (f newFile) create(path string, content []byte) error {
	temp, err := createTemp(path)
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := f.fill(temp, content); err != nil {
		return err
	}
	err = f.link(temp.Name(), path)
	if err == nil || errors.Is(err, fs.ErrExist) {
		return err
	}
	return f.createExclusive(path, content)
}

func createTemp(path string) (*os.File, error) {
	for {
		name := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+strconv.FormatUint(rand.Uint64(), 36))
		file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if !errors.Is(err, fs.ErrExist) {
			return file, err
		}
	}
}

func (f newFile) createExclusive(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if err := f.fill(file, content); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}

func (f newFile) fill(file *os.File, content []byte) error {
	err := f.write(file, content)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}
