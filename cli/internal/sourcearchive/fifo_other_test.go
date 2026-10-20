//go:build !unix

package sourcearchive_test

import "errors"

func mkfifo(string) error {
	return errors.New("no fifos on this platform")
}
