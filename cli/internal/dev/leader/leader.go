package leader

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"
)

type Leader struct {
	Address string
	Token   string
}

var ErrAlreadyRunning = errors.New("another ocel dev already leads this project")

const probeTimeout = 2 * time.Second

func Find(ctx context.Context, configPath string) (Leader, bool, error) {
	leader, err := Read(configPath)
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, ErrMalformed):
		return Leader{}, false, nil
	case err != nil:
		return Leader{}, false, fmt.Errorf("read the dev leader record: %w", err)
	}
	return leader, isAnswering(ctx, leader), nil
}

func Claim(ctx context.Context, configPath string, leader Leader) error {
	lock, err := lockRecord(configPath)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()

	current, err := Read(configPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case errors.Is(err, ErrMalformed):
		if err := removeRecord(configPath); err != nil {
			return err
		}
	case err != nil:
		return fmt.Errorf("read the dev leader record: %w", err)
	case isAnswering(ctx, current):
		return ErrAlreadyRunning
	default:
		if err := removeRecord(configPath); err != nil {
			return err
		}
	}

	if err := writeRecord(configPath, leader); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrAlreadyRunning
		}
		return err
	}
	return nil
}

func Release(configPath, token string) error {
	lock, err := lockRecord(configPath)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()

	current, err := Read(configPath)
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, ErrMalformed):
		return nil
	case err != nil:
		return fmt.Errorf("read the dev leader record: %w", err)
	case current.Token != token:
		return nil
	}
	return removeRecord(configPath)
}

func isAnswering(ctx context.Context, leader Leader) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), probeTimeout)
	defer cancel()
	stream, err := Subscribe(ctx, leader)
	if err != nil {
		return false
	}
	stream.Close()
	return true
}
