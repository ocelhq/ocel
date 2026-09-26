package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
)

const (
	OriginSecretMaxAge = 90 * 24 * time.Hour
	OriginSecretGrace  = 7 * 24 * time.Hour
)

type OriginSecret struct {
	Current   string    `json:"current"`
	CreatedAt time.Time `json:"createdAt"`
	Previous  string    `json:"previous,omitempty"`
	RotatedAt time.Time `json:"rotatedAt,omitzero"`
}

func (s OriginSecret) Held() bool { return s.Current != "" }

func (s OriginSecret) Rotating() bool { return s.Previous != "" }

func (s OriginSecret) Stale(now time.Time) bool {
	return s.Held() && !s.CreatedAt.IsZero() && now.Sub(s.CreatedAt) >= OriginSecretMaxAge
}

func (s OriginSecret) GraceOver(now time.Time) bool {
	return s.Rotating() && now.Sub(s.RotatedAt) >= OriginSecretGrace
}

func (s OriginSecret) Presented(deployedAt int64) string {
	if s.Rotating() && deployedAt < s.RotatedAt.Unix() {
		return s.Previous
	}
	return s.Current
}

func OriginSecretOf(raw string) (OriginSecret, error) {
	var held OriginSecret
	if err := json.Unmarshal([]byte(raw), &held); err != nil {
		return OriginSecret{}, fmt.Errorf("parse origin secret: %w", err)
	}
	if !held.Held() {
		return OriginSecret{}, errors.New("parse origin secret: it names no current secret")
	}
	return held, nil
}

func StaleOriginSecretNotice(s OriginSecret, now time.Time, class string) string {
	switch {
	case s.Rotating():
		return fmt.Sprintf("the %s origin secret was rotated %d days ago; this deploy answers to the new one, and every other project in the class must be re-deployed by %s, when `ocel bootstrap` retires the old one and a release still holding it stops answering",
			class, int(now.Sub(s.RotatedAt).Hours()/24), s.RotatedAt.Add(OriginSecretGrace).UTC().Format(time.DateOnly))
	case s.Stale(now):
		return fmt.Sprintf("the %s origin secret every front presents to reach a release is %d days old; run `ocel bootstrap` to rotate it (secrets older than %d days are rotated there), then re-deploy each project so its releases accept the new one",
			class, int(now.Sub(s.CreatedAt).Hours()/24), int(OriginSecretMaxAge.Hours()/24))
	}
	return ""
}

const (
	originSecretStale   = "it is older than %d days and is rotated; the one it replaces stays accepted for %d days"
	originSecretRetired = "the secret it replaced %d days ago is retired; a release not re-deployed since stops answering its front"
)

func planOriginSecret(ctx context.Context, ssmClient SSMAPI, ns Namespace, class string, now time.Time) (provider.Change, error) {
	name, err := ns.OriginSecretParamFor(class)
	if err != nil {
		return provider.Change{}, err
	}
	held, found, err := readOriginSecret(ctx, ssmClient, name)
	if err != nil {
		return provider.Change{}, err
	}
	change := provider.Change{Kind: kindParameter, Name: name, Action: provider.ActionCreate}
	switch {
	case !found:
	case held.GraceOver(now):
		change.Action, change.Reason = provider.ActionUpdate, fmt.Sprintf(originSecretRetired, int(now.Sub(held.RotatedAt).Hours()/24))
	case held.Stale(now) && !held.Rotating():
		change.Action, change.Reason = provider.ActionUpdate, fmt.Sprintf(originSecretStale, int(OriginSecretMaxAge.Hours()/24), int(OriginSecretGrace.Hours()/24))
	default:
		change.Action, change.Reason = provider.ActionKeep, paramCurrent
	}
	return change, nil
}

type originSecretOutcome struct {
	minted  bool
	rotated bool
	retired bool
}

func ensureOriginSecret(ctx context.Context, ssmClient SSMAPI, ns Namespace, class string, now time.Time) (originSecretOutcome, error) {
	paramName, err := ns.OriginSecretParamFor(class)
	if err != nil {
		return originSecretOutcome{}, err
	}
	description := fmt.Sprintf(
		"Ocel: the shared secret every %s release demands of the front that reaches it, because those Function URLs answer without SigV4, and when it was minted. A rotation keeps the one it replaced here until every release has been re-deployed.",
		class,
	)
	held, found, err := readOriginSecret(ctx, ssmClient, paramName)
	if err != nil {
		return originSecretOutcome{}, err
	}
	if !found {
		minted, err := mintSecret()
		if err != nil {
			return originSecretOutcome{}, fmt.Errorf("generate the secret %s holds: %w", paramName, err)
		}
		created, err := createOriginSecret(ctx, ssmClient, paramName, description, OriginSecret{Current: minted, CreatedAt: stamp(now)})
		if err != nil {
			return originSecretOutcome{}, err
		}
		return originSecretOutcome{minted: created}, nil
	}

	var outcome originSecretOutcome
	if held.GraceOver(now) {
		held.Previous, held.RotatedAt = "", time.Time{}
		outcome.retired = true
	}
	if held.Stale(now) {
		if held.Rotating() {
			return originSecretOutcome{}, fmt.Errorf(
				"the %s origin secret is %d days old and due for rotation, but the one it replaced on %s is still within its %d-day grace, so a second rotation would strand every release that has not been re-deployed since: "+
					"re-deploy every project in the class, wait for %s, and re-run bootstrap",
				class, int(now.Sub(held.CreatedAt).Hours()/24), held.RotatedAt.UTC().Format(time.DateOnly), int(OriginSecretGrace.Hours()/24), held.RotatedAt.Add(OriginSecretGrace).UTC().Format(time.DateOnly))
		}
		minted, err := mintSecret()
		if err != nil {
			return originSecretOutcome{}, fmt.Errorf("generate the secret %s holds: %w", paramName, err)
		}
		held = OriginSecret{Current: minted, CreatedAt: stamp(now), Previous: held.Current, RotatedAt: stamp(now)}
		outcome.minted, outcome.rotated = true, true
	}
	if !outcome.retired && !outcome.rotated {
		return outcome, nil
	}
	if err := writeOriginSecret(ctx, ssmClient, paramName, description, held, true); err != nil {
		return originSecretOutcome{}, err
	}
	return outcome, nil
}

func stamp(now time.Time) time.Time { return now.UTC().Truncate(time.Second) }

func mintSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func readOriginSecret(ctx context.Context, ssmClient SSMAPI, paramName string) (OriginSecret, bool, error) {
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(paramName),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		var notFound *ssmtypes.ParameterNotFound
		if errors.As(err, &notFound) {
			return OriginSecret{}, false, nil
		}
		return OriginSecret{}, false, fmt.Errorf("read %s: %w", paramName, err)
	}
	held, err := OriginSecretOf(aws.ToString(out.Parameter.Value))
	if err != nil {
		return OriginSecret{}, true, fmt.Errorf("%s holds something other than the origin secret bootstrap writes: %w. Delete the parameter and re-run bootstrap to mint a fresh one, then re-deploy every project in the class", paramName, err)
	}
	return held, true, nil
}

func createOriginSecret(ctx context.Context, ssmClient SSMAPI, paramName, description string, held OriginSecret) (bool, error) {
	err := writeOriginSecret(ctx, ssmClient, paramName, description, held, false)
	if err == nil {
		return true, nil
	}
	var exists *ssmtypes.ParameterAlreadyExists
	if !errors.As(err, &exists) {
		return false, err
	}
	if _, _, err := readOriginSecret(ctx, ssmClient, paramName); err != nil {
		return false, fmt.Errorf("read %s a concurrent bootstrap created: %w", paramName, err)
	}
	return false, nil
}

func writeOriginSecret(ctx context.Context, ssmClient SSMAPI, paramName, description string, held OriginSecret, overwrite bool) error {
	payload, err := json.Marshal(held)
	if err != nil {
		return fmt.Errorf("marshal origin secret: %w", err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:        aws.String(paramName),
		Description: aws.String(description),
		Value:       aws.String(string(payload)),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(overwrite),
	}); err != nil {
		var exists *ssmtypes.ParameterAlreadyExists
		if errors.As(err, &exists) {
			return err
		}
		return fmt.Errorf("write %s: %w", paramName, err)
	}
	return nil
}

func ensureSecret(ctx context.Context, ssmClient SSMAPI, paramName, description string) (string, error) {
	read := func() (string, error) {
		out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
			Name:           aws.String(paramName),
			WithDecryption: aws.Bool(true),
		})
		if err != nil {
			return "", err
		}
		return aws.ToString(out.Parameter.Value), nil
	}

	secret, err := read()
	if err == nil {
		return secret, nil
	}
	var notFound *ssmtypes.ParameterNotFound
	if !errors.As(err, &notFound) {
		return "", fmt.Errorf("read %s: %w", paramName, err)
	}

	minted, err := mintSecret()
	if err != nil {
		return "", fmt.Errorf("generate the secret %s holds: %w", paramName, err)
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:        aws.String(paramName),
		Description: aws.String(description),
		Value:       aws.String(minted),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(false),
	}); err != nil {
		var exists *ssmtypes.ParameterAlreadyExists
		if !errors.As(err, &exists) {
			return "", fmt.Errorf("write %s: %w", paramName, err)
		}
		secret, err := read()
		if err != nil {
			return "", fmt.Errorf("read %s a concurrent bootstrap created: %w", paramName, err)
		}
		return secret, nil
	}
	return minted, nil
}
