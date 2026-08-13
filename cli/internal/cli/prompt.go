package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

var (
	stdinMu     sync.Mutex
	stdinReader *bufio.Reader
)

var errStdinBusy = errors.New("stdin is already being read by an abandoned prompt")

func readLine(ctx context.Context, stdin io.Reader) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	resCh := make(chan struct {
		line string
		err  error
	}, 1)
	go func() {
		line, err := readLineFrom(stdin)
		resCh <- struct {
			line string
			err  error
		}{line, err}
	}()

	select {
	case r := <-resCh:
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
			return r.line, r.err
		}
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func readLineFrom(stdin io.Reader) (string, error) {
	var line string
	var err error
	if stdin == os.Stdin {
		if !stdinMu.TryLock() {
			return "", errStdinBusy
		}
		if stdinReader == nil {
			stdinReader = bufio.NewReader(os.Stdin)
		}
		line, err = stdinReader.ReadString('\n')
		stdinMu.Unlock()
	} else {
		line, err = bufio.NewReader(stdin).ReadString('\n')
	}

	line = strings.TrimRight(line, "\r\n")
	if err == io.EOF && line != "" {
		err = nil
	}
	if err != nil && err != io.EOF {
		err = fmt.Errorf("failed to read input: %w", err)
	}
	return line, err
}

func confirmYN(ctx context.Context, prompt string, stdout io.Writer, stdin io.Reader) (bool, error) {
	fmt.Fprintf(stdout, "%s [y/N] ", prompt)

	line, err := readLine(ctx, stdin)
	if err != nil {
		if err == io.EOF {
			return false, nil
		}
		return false, err
	}

	answer := strings.TrimSpace(line)
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), nil
}
