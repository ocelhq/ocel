package terminal

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

var ErrStdinBusy = errors.New("another prompt is already reading stdin")

func (p Prompt) confirmLine(ctx context.Context, question string) (bool, error) {
	fmt.Fprintf(p.out, "%s [y/N] ", question)

	line, err := p.readLine(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		return false, err
	}

	answer := strings.TrimSpace(line)
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), nil
}

func (p Prompt) phraseLine(ctx context.Context, label, phrase string) (bool, error) {
	fmt.Fprintf(p.out, "Type the %s (%s) to confirm: ", label, phrase)

	line, err := p.readLine(ctx)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		return false, err
	}
	return phrase != "" && strings.TrimSpace(line) == phrase, nil
}

func (p Prompt) selectLine(ctx context.Context, title string, options []Option) ([]string, bool, error) {
	chosen := selectedNames(options)
	for {
		fmt.Fprintf(p.out, "%s:\n", title)
		for i, o := range options {
			mark := " "
			if slices.Contains(chosen, o.Name) {
				mark = "x"
			}
			fmt.Fprintf(p.out, "  %d) [%s] %s\n", i+1, mark, o.label())
		}
		fmt.Fprint(p.out, "Numbers to toggle, comma separated, or Enter to take this set: ")

		line, err := p.readLine(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
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

func (p Prompt) selectOneLine(ctx context.Context, title string, options []Option) (string, bool, error) {
	for {
		fmt.Fprintf(p.out, "%s:\n", title)
		for i, o := range options {
			fmt.Fprintf(p.out, "  %d) %s\n", i+1, o.label())
		}
		fmt.Fprint(p.out, "Number of your choice: ")

		line, err := p.readLine(ctx)
		if errors.Is(err, io.EOF) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		index, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || index < 1 || index > len(options) {
			fmt.Fprintf(p.out, "%q is not one of 1-%d\n", strings.TrimSpace(line), len(options))
			continue
		}
		return options[index-1].Name, true, nil
	}
}

func (p Prompt) inputLine(ctx context.Context, title, description string) (string, bool, error) {
	fmt.Fprintf(p.out, "%s (%s): ", title, description)
	line, err := p.readLine(ctx)
	if errors.Is(err, io.EOF) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(line), true, nil
}

func (p Prompt) awaitEnterLine(ctx context.Context, question string) (bool, error) {
	fmt.Fprintf(p.out, "%s ", question)
	line, err := p.readLine(ctx)
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	answer := strings.TrimSpace(line)
	return !strings.EqualFold(answer, "n") && !strings.EqualFold(answer, "no"), nil
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

func (p Prompt) readLine(ctx context.Context) (string, error) {
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
		line, err = readOneLine(stdin)
	}

	line = strings.TrimRight(line, "\r\n")
	if errors.Is(err, io.EOF) && line != "" {
		err = nil
	}
	if err != nil && !errors.Is(err, io.EOF) {
		err = fmt.Errorf("failed to read input: %w", err)
	}
	return line, err
}

func readOneLine(r io.Reader) (string, error) {
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := r.Read(b)
		if n > 0 {
			line = append(line, b[0])
			if b[0] == '\n' {
				return string(line), nil
			}
		}
		if err != nil {
			return string(line), err
		}
	}
}
