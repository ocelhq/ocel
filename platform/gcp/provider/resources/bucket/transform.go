package bucket

import (
	"fmt"
	"strconv"
	"time"

	"cloud.google.com/go/storage"

	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

const (
	transformKind    = "bucket"
	transformSurface = "bucket"

	defaultStorageClass   = "STANDARD"
	defaultSoftDeleteDays = 7

	lifecycleDate = time.DateOnly
)

var bucketSurface = surface.Surface[storage.BucketAttrs, storage.BucketAttrsToUpdate]{
	Name: transformSurface,
	Fields: []surface.Field[storage.BucketAttrs, storage.BucketAttrsToUpdate]{
		{
			Name: "storage class",
			Paths: []surface.Path[storage.BucketAttrs]{surface.NewPath("storageClass", func(desired *storage.BucketAttrs, class string) error {
				desired.StorageClass = class
				return nil
			})},
			Reset: func(desired *storage.BucketAttrs) {
				if desired.StorageClass == "" {
					desired.StorageClass = defaultStorageClass
				}
			},
			Same: func(current, desired *storage.BucketAttrs) bool { return current.StorageClass == desired.StorageClass },
			Update: func(update *storage.BucketAttrsToUpdate, _, desired *storage.BucketAttrs) {
				update.StorageClass = desired.StorageClass
			},
			Show: func(attrs *storage.BucketAttrs) string { return attrs.StorageClass },
		},
		{
			Name: "versioning",
			Paths: []surface.Path[storage.BucketAttrs]{surface.NewPath("versioning.enabled", func(desired *storage.BucketAttrs, enabled bool) error {
				desired.VersioningEnabled = enabled
				return nil
			})},
			Same: func(current, desired *storage.BucketAttrs) bool {
				return current.VersioningEnabled == desired.VersioningEnabled
			},
			Update: func(update *storage.BucketAttrsToUpdate, _, desired *storage.BucketAttrs) {
				update.VersioningEnabled = desired.VersioningEnabled
			},
			Show: func(attrs *storage.BucketAttrs) string { return surface.ShowOnOff(attrs.VersioningEnabled) },
		},
		{
			Name: "soft delete retention",
			Paths: []surface.Path[storage.BucketAttrs]{surface.NewPath("softDeletePolicy.retentionDurationSeconds", func(desired *storage.BucketAttrs, seconds int64) error {
				if seconds < 0 {
					return fmt.Errorf("%d seconds is negative", seconds)
				}
				desired.SoftDeletePolicy = &storage.SoftDeletePolicy{RetentionDuration: time.Duration(seconds) * time.Second}
				return nil
			})},
			Reset: func(desired *storage.BucketAttrs) {
				if desired.SoftDeletePolicy == nil {
					desired.SoftDeletePolicy = &storage.SoftDeletePolicy{RetentionDuration: defaultSoftDeleteDays * 24 * time.Hour}
				}
			},
			Same: func(current, desired *storage.BucketAttrs) bool {
				return softDeleteRetention(current) == softDeleteRetention(desired)
			},
			Update: func(update *storage.BucketAttrsToUpdate, _, desired *storage.BucketAttrs) {
				update.SoftDeletePolicy = &storage.SoftDeletePolicy{RetentionDuration: softDeleteRetention(desired)}
			},
			Show: func(attrs *storage.BucketAttrs) string { return softDeleteRetention(attrs).String() },
		},
		{
			Name: "retention period",
			Paths: []surface.Path[storage.BucketAttrs]{surface.NewPath("retentionPolicy.retentionPeriod", func(desired *storage.BucketAttrs, period string) error {
				seconds, err := strconv.ParseInt(period, 10, 64)
				if err != nil || seconds <= 0 {
					return fmt.Errorf("%q is not a whole number of seconds above zero", period)
				}
				desired.RetentionPolicy = &storage.RetentionPolicy{RetentionPeriod: time.Duration(seconds) * time.Second}
				return nil
			})},
			Same: func(current, desired *storage.BucketAttrs) bool {
				return retentionPeriod(current) == retentionPeriod(desired)
			},
			Update: func(update *storage.BucketAttrsToUpdate, _, desired *storage.BucketAttrs) {
				update.RetentionPolicy = &storage.RetentionPolicy{RetentionPeriod: retentionPeriod(desired)}
			},
			Show: func(attrs *storage.BucketAttrs) string { return retentionPeriod(attrs).String() },
		},
		{
			Name: "default encryption key",
			Paths: []surface.Path[storage.BucketAttrs]{surface.NewPath("encryption.defaultKmsKeyName", func(desired *storage.BucketAttrs, key string) error {
				desired.Encryption = &storage.BucketEncryption{DefaultKMSKeyName: key}
				return nil
			})},
			Same: func(current, desired *storage.BucketAttrs) bool {
				return encryptionKey(current) == encryptionKey(desired)
			},
			Update: func(update *storage.BucketAttrsToUpdate, _, desired *storage.BucketAttrs) {
				update.Encryption = &storage.BucketEncryption{DefaultKMSKeyName: encryptionKey(desired)}
			},
			Show: func(attrs *storage.BucketAttrs) string { return orGoogleManaged(encryptionKey(attrs)) },
		},
		{
			Name: "access logging",
			Paths: []surface.Path[storage.BucketAttrs]{
				surface.NewPath("logging.logBucket", func(desired *storage.BucketAttrs, bucket string) error {
					desired.Logging = withLogging(desired.Logging, func(logging *storage.BucketLogging) { logging.LogBucket = bucket })
					return nil
				}),
				surface.NewPath("logging.logObjectPrefix", func(desired *storage.BucketAttrs, prefix string) error {
					desired.Logging = withLogging(desired.Logging, func(logging *storage.BucketLogging) { logging.LogObjectPrefix = prefix })
					return nil
				}),
			},
			Same: sameLogging,
			Update: func(update *storage.BucketAttrsToUpdate, current, desired *storage.BucketAttrs) {
				update.Logging = withLogging(desired.Logging, func(logging *storage.BucketLogging) {
					if logging.LogBucket != "" && logging.LogObjectPrefix == "" {
						logging.LogObjectPrefix = current.Name
					}
				})
			},
			Show: func(attrs *storage.BucketAttrs) string {
				if attrs.Logging == nil || attrs.Logging.LogBucket == "" {
					return "off"
				}
				return "to " + attrs.Logging.LogBucket
			},
		},
		{
			Name: "lifecycle rules",
			Paths: []surface.Path[storage.BucketAttrs]{surface.NewPath("lifecycleRules", func(desired *storage.BucketAttrs, rules []pulumiLifecycleRule) error {
				for _, rule := range rules {
					translated, err := rule.translate()
					if err != nil {
						return err
					}
					desired.Lifecycle.Rules = append(desired.Lifecycle.Rules, translated)
				}
				return nil
			})},
		},
	},
	Labels: &surface.Labels[storage.BucketAttrs, storage.BucketAttrsToUpdate]{
		Of: func(attrs *storage.BucketAttrs) *map[string]string { return &attrs.Labels },
		Update: func(update *storage.BucketAttrsToUpdate, set map[string]string, removed []string) {
			for key, value := range set {
				update.SetLabel(key, value)
			}
			for _, key := range removed {
				update.DeleteLabel(key)
			}
		},
	},
}

func softDeleteRetention(attrs *storage.BucketAttrs) time.Duration {
	if attrs.SoftDeletePolicy == nil {
		return 0
	}
	return attrs.SoftDeletePolicy.RetentionDuration
}

func retentionPeriod(attrs *storage.BucketAttrs) time.Duration {
	if attrs.RetentionPolicy == nil {
		return 0
	}
	return attrs.RetentionPolicy.RetentionPeriod
}

func encryptionKey(attrs *storage.BucketAttrs) string {
	if attrs.Encryption == nil {
		return ""
	}
	return attrs.Encryption.DefaultKMSKeyName
}

func orGoogleManaged(key string) string {
	if key == "" {
		return "Google-managed"
	}
	return key
}

func withLogging(current *storage.BucketLogging, change func(*storage.BucketLogging)) *storage.BucketLogging {
	logging := storage.BucketLogging{}
	if current != nil {
		logging = *current
	}
	change(&logging)
	return &logging
}

func sameLogging(current, desired *storage.BucketAttrs) bool {
	held, wanted := withLogging(current.Logging, func(*storage.BucketLogging) {}), withLogging(desired.Logging, func(*storage.BucketLogging) {})
	if held.LogBucket != wanted.LogBucket {
		return false
	}
	if wanted.LogObjectPrefix == "" {
		return held.LogObjectPrefix == "" || held.LogObjectPrefix == current.Name
	}
	return held.LogObjectPrefix == wanted.LogObjectPrefix
}

type pulumiLifecycleRule struct {
	Action struct {
		Type         string `json:"type"`
		StorageClass string `json:"storageClass"`
	} `json:"action"`
	Condition struct {
		Age                     *int64   `json:"age"`
		CreatedBefore           string   `json:"createdBefore"`
		CustomTimeBefore        string   `json:"customTimeBefore"`
		DaysSinceCustomTime     int64    `json:"daysSinceCustomTime"`
		DaysSinceNoncurrentTime int64    `json:"daysSinceNoncurrentTime"`
		MatchesPrefixes         []string `json:"matchesPrefixes"`
		MatchesStorageClasses   []string `json:"matchesStorageClasses"`
		MatchesSuffixes         []string `json:"matchesSuffixes"`
		NoncurrentTimeBefore    string   `json:"noncurrentTimeBefore"`
		NumNewerVersions        int64    `json:"numNewerVersions"`
		WithState               string   `json:"withState"`
	} `json:"condition"`
}

func (r pulumiLifecycleRule) translate() (storage.LifecycleRule, error) {
	if r.Action.Type == "" {
		return storage.LifecycleRule{}, fmt.Errorf("a lifecycle rule names no action type")
	}
	condition := storage.LifecycleCondition{
		DaysSinceCustomTime:     r.Condition.DaysSinceCustomTime,
		DaysSinceNoncurrentTime: r.Condition.DaysSinceNoncurrentTime,
		MatchesPrefix:           r.Condition.MatchesPrefixes,
		MatchesStorageClasses:   r.Condition.MatchesStorageClasses,
		MatchesSuffix:           r.Condition.MatchesSuffixes,
		NumNewerVersions:        r.Condition.NumNewerVersions,
	}
	if age := r.Condition.Age; age != nil {
		condition.AgeInDays = *age
		condition.AllObjects = *age == 0
	}
	for _, date := range []struct {
		written string
		into    *time.Time
	}{
		{r.Condition.CreatedBefore, &condition.CreatedBefore},
		{r.Condition.CustomTimeBefore, &condition.CustomTimeBefore},
		{r.Condition.NoncurrentTimeBefore, &condition.NoncurrentTimeBefore},
	} {
		if date.written == "" {
			continue
		}
		parsed, err := time.Parse(lifecycleDate, date.written)
		if err != nil {
			return storage.LifecycleRule{}, fmt.Errorf("a lifecycle rule's date %q is not YYYY-MM-DD", date.written)
		}
		*date.into = parsed
	}
	switch r.Condition.WithState {
	case "", "ANY":
		condition.Liveness = storage.LiveAndArchived
	case "LIVE":
		condition.Liveness = storage.Live
	case "ARCHIVED":
		condition.Liveness = storage.Archived
	default:
		return storage.LifecycleRule{}, fmt.Errorf("a lifecycle rule's withState %q is none of LIVE, ARCHIVED and ANY", r.Condition.WithState)
	}
	return storage.LifecycleRule{
		Action:    storage.LifecycleAction{Type: r.Action.Type, StorageClass: r.Action.StorageClass},
		Condition: condition,
	}, nil
}

func transformTarget() *component.Transform {
	return &component.Transform{Kind: transformKind, Allowed: surface.Allowed{transformSurface: bucketSurface.Paths()}}
}
