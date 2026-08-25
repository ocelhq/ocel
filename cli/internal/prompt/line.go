package prompt

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
)

var (
	stdinMu     sync.Mutex
	stdinReader *bufio.Reader
)

var ErrStdinBusy = errors.New("stdin is already being read by an abandoned prompt")

func (p Prompter) confirmLine(ctx context.Context, question string) (bool, error) {
	fmt.Fprintf(p.out, "%s [y/N] ", question)

	line, err := p.readLine(ctx)
	if err != nil {
		if err == io.EOF {
			return false, nil
		}
		return false, err
	}

	answer := strings.TrimSpace(line)
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), nil
}

func (p Prompter) phraseLine(ctx context.Context, label, phrase string) (bool, error) {
	fmt.Fprintf(p.out, "Type the %s (%s) to confirm: ", label, phrase)

	line, err := p.readLine(ctx)
	if err != nil {
		if err == io.EOF {
			return false, nil
		}
		return false, err
	}
	return phrase != "" && strings.TrimSpace(line) == phrase, nil
}

func (p Prompter) selectLine(ctx context.Context, title string, options []Option) ([]string, bool, error) {
	chosen := selectedNames(options)
	for {
		fmt.Fprintf(p.out, "%s:\n", title)
		for i, o := range options {
			mark := " "
			if slices.Contains(chosen, o.Name) {
				mark = "x"
			}
			fmt.Fprintf(p.out, "  %d) [%s] %s", i+1, mark, o.Name)
			if o.Summary != "" {
				fmt.Fprintf(p.out, " — %s", o.Summary)
			}
			fmt.Fprintln(p.out)
		}
		fmt.Fprint(p.out, "Numbers to toggle, comma separated, or Enter to take this set: ")

		line, err := p.readLine(ctx)
		if err != nil {
			if err == io.EOF {
				return nil, false, nil
			}
			return nil, false, err
		}
		if strings.TrimSpace(line) == "" {
			return chosen, true, nil
		}
		toggled, err := toggle(options, chosen, line)
		if err != nil {
			fmt.Fprintln(p.out, err)
			continue
		}
		chosen = toggled
	}
}

func toggle(options []Option, chosen []string, line string) ([]string, error) {
	next := slices.Clone(chosen)
	for _, field := range strings.Split(line, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		index, err := strconv.Atoi(field)
		if err != nil || index < 1 || index > len(options) {
			return nil, fmt.Errorf("%q is not one of 1-%d", field, len(options))
		}
		name := options[index-1].Name
		if at := slices.Index(next, name); at >= 0 {
			next = slices.Delete(next, at, at+1)
			continue
		}
		next = append(next, name)
	}
	return next, nil
}

func (p Prompter) readLine(ctx context.Context) (string, error) {
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
		line, err := readLineFrom(p.in)
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
			return "", ErrStdinBusy
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
