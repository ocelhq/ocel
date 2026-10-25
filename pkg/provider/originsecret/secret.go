package originsecret

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	MaxAge = 90 * 24 * time.Hour
	Grace  = 7 * 24 * time.Hour

	secretBytes = 32
)

const (
	reasonStale           = "older than %d days; the old one stays accepted for %d days"
	reasonRetired         = "retires the one replaced %d days ago: a release not re-deployed since stops answering"
	reasonRetiredAndStale = "rotates, and retires the one replaced %d days ago: a release not re-deployed since stops answering"
)

type Secret struct {
	Current   string    `json:"current"`
	CreatedAt time.Time `json:"createdAt"`
	Previous  string    `json:"previous,omitempty"`
	RotatedAt time.Time `json:"rotatedAt,omitzero"`
}

type Renewal struct {
	Rotated bool
	Retired bool
}

func (r Renewal) Changed() bool { return r.Rotated || r.Retired }

func New(now time.Time) (Secret, error) {
	buf := make([]byte, secretBytes)
	if _, err := rand.Read(buf); err != nil {
		return Secret{}, fmt.Errorf("generate an origin secret: %w", err)
	}
	return Secret{Current: hex.EncodeToString(buf), CreatedAt: stamp(now)}, nil
}

func Parse(raw []byte) (Secret, error) {
	var secret Secret
	if err := json.Unmarshal(raw, &secret); err != nil {
		return Secret{}, fmt.Errorf("parse origin secret: %w", err)
	}
	if !secret.Present() {
		return Secret{}, errors.New("parse origin secret: it names no current secret")
	}
	return secret, nil
}

func (s Secret) Present() bool { return s.Current != "" }

func (s Secret) Rotating() bool { return s.Previous != "" }

func (s Secret) Stale(now time.Time) bool {
	return s.Present() && !s.CreatedAt.IsZero() && now.Sub(s.CreatedAt) >= MaxAge
}

func (s Secret) GraceOver(now time.Time) bool {
	return s.Rotating() && now.Sub(s.RotatedAt) >= Grace
}

func (s Secret) Renew(now time.Time) (Secret, Renewal, error) {
	var renewal Renewal
	if s.GraceOver(now) {
		s.Previous, s.RotatedAt = "", time.Time{}
		renewal.Retired = true
	}
	if !s.Stale(now) {
		return s, renewal, nil
	}
	successor, err := New(now)
	if err != nil {
		return Secret{}, Renewal{}, err
	}
	successor.Previous, successor.RotatedAt = s.Current, successor.CreatedAt
	renewal.Rotated = true
	return successor, renewal, nil
}

func (s Secret) PlanChange(now time.Time) (provider.ChangeAction, string) {
	switch {
	case !s.Present():
		return provider.ActionCreate, ""
	case s.GraceOver(now) && s.Stale(now):
		return provider.ActionUpdate, fmt.Sprintf(reasonRetiredAndStale, days(now.Sub(s.RotatedAt)))
	case s.GraceOver(now):
		return provider.ActionUpdate, fmt.Sprintf(reasonRetired, days(now.Sub(s.RotatedAt)))
	case s.Stale(now):
		return provider.ActionUpdate, fmt.Sprintf(reasonStale, days(MaxAge), days(Grace))
	}
	return provider.ActionKeep, provider.ReasonCurrent
}

func FormatStaleNotice(s Secret, now time.Time, tier environment.Tier) string {
	switch {
	case s.GraceOver(now):
		return fmt.Sprintf("The %s origin secret was rotated %d days ago and the old one's %d-day grace has run: the next `ocel bootstrap` retires it, and a release still expecting it stops answering, so re-deploy every other project in the tier first",
			tier, days(now.Sub(s.RotatedAt)), days(Grace))
	case s.Rotating():
		return fmt.Sprintf("The %s origin secret was rotated %d days ago; this deploy answers to the new one, and every other project in the tier must be re-deployed by %s, when `ocel bootstrap` retires the old one and a release still expecting it stops answering",
			tier, days(now.Sub(s.RotatedAt)), s.RotatedAt.Add(Grace).UTC().Format(time.DateOnly))
	case s.Stale(now):
		return fmt.Sprintf("The %s origin secret every front presents to reach a release is %d days old; run `ocel bootstrap` to rotate it (secrets older than %d days are rotated there), then re-deploy each project so its releases accept the new one",
			tier, days(now.Sub(s.CreatedAt)), days(MaxAge))
	}
	return ""
}

func days(d time.Duration) int { return int(d.Hours() / 24) }

func stamp(now time.Time) time.Time { return now.UTC().Truncate(time.Second) }
