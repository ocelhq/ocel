package topics

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	cloudscheduler "google.golang.org/api/cloudscheduler/v1"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

const (
	defaultMinBackoff   = 5 * time.Second
	defaultMaxBackoff   = time.Hour
	defaultMaxDoublings = 5
)

type JobUpdate struct {
	Job  cloudscheduler.Job
	Mask []string
}

func (u *JobUpdate) send(field string) { u.Mask = append(u.Mask, field) }

var JobSurface = surface.Surface[cloudscheduler.Job, JobUpdate]{
	Name: "job",
	Fields: []surface.Field[cloudscheduler.Job, JobUpdate]{
		{
			Name: "retry policy",
			Paths: []surface.Path[cloudscheduler.Job]{
				surface.NewPath("retryConfig.retryCount", func(desired *cloudscheduler.Job, count int64) error {
					if count < 0 {
						return fmt.Errorf("%d retries is negative", count)
					}
					setRetry(desired, "RetryCount", func(retry *cloudscheduler.RetryConfig) { retry.RetryCount = count })
					return nil
				}),
				surface.NewPath("retryConfig.maxDoublings", func(desired *cloudscheduler.Job, doublings int64) error {
					if doublings < 0 {
						return fmt.Errorf("%d doublings is negative", doublings)
					}
					setRetry(desired, "MaxDoublings", func(retry *cloudscheduler.RetryConfig) { retry.MaxDoublings = doublings })
					return nil
				}),
				retryDuration("retryConfig.maxRetryDuration", "MaxRetryDuration", func(retry *cloudscheduler.RetryConfig, d string) { retry.MaxRetryDuration = d }),
				retryDuration("retryConfig.minBackoffDuration", "MinBackoffDuration", func(retry *cloudscheduler.RetryConfig, d string) { retry.MinBackoffDuration = d }),
				retryDuration("retryConfig.maxBackoffDuration", "MaxBackoffDuration", func(retry *cloudscheduler.RetryConfig, d string) { retry.MaxBackoffDuration = d }),
			},
			Same: func(current, desired *cloudscheduler.Job) bool {
				return retryPolicyOf(current.RetryConfig) == retryPolicyOf(desired.RetryConfig)
			},
			Update: func(update *JobUpdate, _, desired *cloudscheduler.Job) {
				update.Job.RetryConfig = desired.RetryConfig
				update.send("retryConfig")
			},
			Show: func(job *cloudscheduler.Job) string { return retryPolicyOf(job.RetryConfig).String() },
		},
		{
			Name: "description",
			Paths: []surface.Path[cloudscheduler.Job]{surface.NewPath("description", func(desired *cloudscheduler.Job, description string) error {
				desired.Description = description
				return nil
			})},
			Same: func(current, desired *cloudscheduler.Job) bool { return current.Description == desired.Description },
			Update: func(update *JobUpdate, _, desired *cloudscheduler.Job) {
				update.Job.Description = desired.Description
				update.send("description")
			},
			Show: func(job *cloudscheduler.Job) string { return strconv.Quote(job.Description) },
		},
	},
}

func setRetry(desired *cloudscheduler.Job, field string, change func(*cloudscheduler.RetryConfig)) {
	if desired.RetryConfig == nil {
		desired.RetryConfig = &cloudscheduler.RetryConfig{}
	}
	change(desired.RetryConfig)
	if !slices.Contains(desired.RetryConfig.ForceSendFields, field) {
		desired.RetryConfig.ForceSendFields = append(desired.RetryConfig.ForceSendFields, field)
	}
}

func retryDuration(path, field string, set func(*cloudscheduler.RetryConfig, string)) surface.Path[cloudscheduler.Job] {
	return surface.NewPath(path, func(desired *cloudscheduler.Job, written string) error {
		if _, err := parseSeconds(written); err != nil {
			return err
		}
		setRetry(desired, field, func(retry *cloudscheduler.RetryConfig) { set(retry, written) })
		return nil
	})
}

func parseSeconds(written string) (time.Duration, error) {
	seconds, found := strings.CutSuffix(written, "s")
	parsed, err := strconv.ParseFloat(seconds, 64)
	if !found || err != nil || parsed < 0 {
		return 0, fmt.Errorf("%q is not a number of seconds ending in s, such as 30s", written)
	}
	return time.Duration(parsed * float64(time.Second)), nil
}

type retryPolicy struct {
	count       int64
	doublings   int64
	within      time.Duration
	backoffFrom time.Duration
	backoffTo   time.Duration
}

func retryPolicyOf(retry *cloudscheduler.RetryConfig) retryPolicy {
	policy := retryPolicy{doublings: defaultMaxDoublings, backoffFrom: defaultMinBackoff, backoffTo: defaultMaxBackoff}
	if retry == nil {
		return policy
	}
	patched := len(retry.ForceSendFields) > 0
	set := func(field string) bool { return !patched || slices.Contains(retry.ForceSendFields, field) }
	if set("RetryCount") {
		policy.count = retry.RetryCount
	}
	if set("MaxDoublings") {
		policy.doublings = retry.MaxDoublings
	}
	for _, duration := range []struct {
		field   string
		written string
		into    *time.Duration
	}{
		{"MaxRetryDuration", retry.MaxRetryDuration, &policy.within},
		{"MinBackoffDuration", retry.MinBackoffDuration, &policy.backoffFrom},
		{"MaxBackoffDuration", retry.MaxBackoffDuration, &policy.backoffTo},
	} {
		if parsed, err := parseSeconds(duration.written); err == nil && set(duration.field) {
			*duration.into = parsed
		}
	}
	return policy
}

func (p retryPolicy) String() string {
	described := fmt.Sprintf("%d retries", p.count)
	if p.backoffFrom != defaultMinBackoff || p.backoffTo != defaultMaxBackoff || p.doublings != defaultMaxDoublings {
		described += fmt.Sprintf(" backing off from %s to %s over %d doublings", p.backoffFrom, p.backoffTo, p.doublings)
	}
	if p.within > 0 {
		described += " within " + p.within.String()
	}
	return described
}
