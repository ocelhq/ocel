package alb

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mathrand "math/rand/v2"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	leaseTerm         = 5 * time.Minute
	leaseWorkWindow   = leaseTerm - time.Minute
	leaseAttempts     = 12
	leaseWaitBase     = 250 * time.Millisecond
	leaseWaitCeiling  = 5 * time.Second
	conflictErrorCode = "ConditionalRequestConflict"
)

type lease struct {
	c        Clients
	key      string
	activity string
	pause    func(context.Context, time.Duration) error
}

type leaseRecord struct {
	Holder string `json:"holder"`
	Until  int64  `json:"until"`
}

func formatTrustLeaseKey(tier environment.Tier) string {
	return "ocel/trust/" + string(tier) + "/client-cas.lease"
}

func (s *stack) newLease(c Clients, key, activity string) lease {
	return lease{c: c, key: key, activity: activity, pause: s.pause}
}

func (l lease) hold(ctx context.Context, work func(context.Context) error) error {
	holder, err := newHolder()
	if err != nil {
		return err
	}
	etag, acquired, err := l.acquire(ctx, holder)
	if err != nil {
		return err
	}
	bounded, stop := context.WithDeadline(ctx, acquired.Add(leaseWorkWindow))
	worked := work(bounded)
	stop()
	return errors.Join(worked, l.release(context.WithoutCancel(ctx), etag))
}

func newHolder() (string, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	return hex.EncodeToString(id), nil
}

func (l lease) acquire(ctx context.Context, holder string) (string, time.Time, error) {
	for attempt := range leaseAttempts {
		if attempt > 0 {
			if err := l.pause(ctx, computeLeaseDelay(attempt-1)); err != nil {
				return "", time.Time{}, err
			}
		}
		current, etag, err := l.read(ctx)
		if err != nil {
			return "", time.Time{}, err
		}
		acquired := time.Now()
		if etag != "" && acquired.Unix() < current.Until {
			continue
		}
		body, err := json.Marshal(leaseRecord{Holder: holder, Until: acquired.Add(leaseTerm).Unix()})
		if err != nil {
			return "", time.Time{}, err
		}
		put := &s3.PutObjectInput{Bucket: aws.String(l.c.Bucket), Key: aws.String(l.key), Body: bytes.NewReader(body)}
		if etag == "" {
			put.IfNoneMatch = aws.String("*")
		} else {
			put.IfMatch = aws.String(etag)
		}
		written, err := l.c.Objects.PutObject(ctx, put)
		if isConditionFailed(err) {
			continue
		}
		if err != nil {
			return "", time.Time{}, fmt.Errorf("record that this deploy is %s: %w", l.activity, err)
		}
		return aws.ToString(written.ETag), acquired, nil
	}
	return "", time.Time{}, refusal.Refuse(refusal.CodeBusy,
		"another deploy was %s on every one of %d attempts, and one deploy does that at a time so that neither undoes the other: run this again once the other one has finished",
		l.activity, leaseAttempts)
}

func (l lease) read(ctx context.Context) (leaseRecord, string, error) {
	read, err := l.c.Objects.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(l.c.Bucket), Key: aws.String(l.key)})
	var absent *s3types.NoSuchKey
	if errors.As(err, &absent) {
		return leaseRecord{}, "", nil
	}
	if err != nil {
		return leaseRecord{}, "", fmt.Errorf("read which deploy is %s: %w", l.activity, err)
	}
	defer read.Body.Close()
	body, err := io.ReadAll(read.Body)
	if err != nil {
		return leaseRecord{}, "", fmt.Errorf("read which deploy is %s: %w", l.activity, err)
	}
	var current leaseRecord
	if err := json.Unmarshal(body, &current); err != nil {
		return leaseRecord{}, "", fmt.Errorf("read which deploy is %s: %w", l.activity, err)
	}
	return current, aws.ToString(read.ETag), nil
}

func (l lease) release(ctx context.Context, etag string) error {
	_, err := l.c.Objects.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(l.c.Bucket), Key: aws.String(l.key), IfMatch: aws.String(etag)})
	var absent *s3types.NoSuchKey
	if err == nil || isConditionFailed(err) || errors.As(err, &absent) {
		return nil
	}
	return fmt.Errorf("record that this deploy is no longer %s: %w", l.activity, err)
}

func isConditionFailed(err error) bool {
	var api smithy.APIError
	return errors.As(err, &api) && (api.ErrorCode() == preconditionError || api.ErrorCode() == conflictErrorCode)
}

func computeLeaseDelay(attempt int) time.Duration {
	delay := min(leaseWaitBase<<attempt, leaseWaitCeiling)
	return delay - time.Duration(mathrand.Float64()*float64(delay/4))
}
