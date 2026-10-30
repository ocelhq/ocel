package host

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"sync"

	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

var errNoAssets = errors.New("this build carries none of the binaries a bootstrap installs on a box")

type Assets struct {
	files       fs.FS
	realtimeSum func() string
	namesOnly   bool
}

var itemNamesOnly = Assets{namesOnly: true}

func NewAssets(files fs.FS) Assets {
	a := Assets{files: files}
	a.realtimeSum = sync.OnceValue(func() string {
		sum := sha256.New()
		for _, arch := range []string{ssh.AMD64, ssh.ARM64} {
			sum.Write(a.binary(realtimeBinaryName, arch))
		}
		return hex.EncodeToString(sum.Sum(nil))
	})
	return a
}

func (a Assets) read(name string) ([]byte, error) {
	if a.files == nil {
		return nil, errNoAssets
	}
	return fs.ReadFile(a.files, name)
}

func (a Assets) stat(name string) error {
	if a.files == nil {
		return errNoAssets
	}
	_, err := fs.Stat(a.files, name)
	return err
}

func (a Assets) binary(name, arch string) []byte {
	if a.namesOnly {
		return nil
	}
	read, err := a.read("dist/" + name + "-" + arch)
	if err != nil {
		panic(err)
	}
	return read
}

func (a Assets) RealtimeBinarySum() string {
	if a.realtimeSum == nil {
		panic(errNoAssets)
	}
	return a.realtimeSum()
}
