package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
)

type lineResult struct {
	line string
	ok   bool
	err  error
}

func readLine(ctx context.Context, stdin io.Reader) (string, bool, error) {
	resCh := make(chan lineResult, 1)
	go func() {
		scanner := bufio.NewScanner(stdin)
		ok := scanner.Scan()
		resCh <- lineResult{line: scanner.Text(), ok: ok, err: scanner.Err()}
	}()

	select {
	case <-ctx.Done():
		return "", false, ctx.Err()
	case r := <-resCh:
		if r.err != nil {
			return "", false, fmt.Errorf("failed to read input: %w", r.err)
		}
		return r.line, r.ok, nil
	}
}

func confirmYN(ctx context.Context, prompt string, stdout io.Writer, stdin io.Reader) (bool, error) {
	fmt.Fprintf(stdout, "%s [y/N] ", prompt)

	line, ok, err := readLine(ctx, stdin)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}

	answer := strings.TrimSpace(line)
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), nil
}
