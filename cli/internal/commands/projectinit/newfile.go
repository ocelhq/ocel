package projectinit

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return err
	}
	if err := f.fill(temp, content); err != nil {
		return err
	}
	err = f.link(temp.Name(), path)
	if err == nil || errors.Is(err, fs.ErrExist) {
		return err
	}
	return f.createExclusive(path, content)
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
