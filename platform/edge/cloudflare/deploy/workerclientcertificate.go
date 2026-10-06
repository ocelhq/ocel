package cloudflare

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	cf "github.com/cloudflare/cloudflare-go/v4"
	"github.com/cloudflare/cloudflare-go/v4/mtls_certificates"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
)

const (
	workerClientCertificateRenewal = 182 * 24 * time.Hour
	workerClientCertificateStem    = "origin-client"
	workerClientCertificateDate    = "20060102"
	workerClientCertificateDay     = "2006-01-02"
)

var workerClientCertificatePermissions = []string{"Account · SSL and Certificates · Edit"}

type workerClientCertificates struct {
	client    *cf.Client
	namespace string
}

func (p *cloudflare) workerClientCertificates() workerClientCertificates {
	return workerClientCertificates{client: p.client, namespace: p.namespace}
}

type heldWorkerClientCertificate struct {
	id           string
	name         string
	certificates string
	expiresOn    time.Time
}

type workerClientCertificateState struct {
	enabled bool
	base    string
	now     time.Time
	held    []heldWorkerClientCertificate
}

func (s workerClientCertificateState) current() (heldWorkerClientCertificate, bool) {
	if len(s.held) == 0 {
		return heldWorkerClientCertificate{}, false
	}
	return s.held[0], true
}

func (s workerClientCertificateState) keptID() string {
	if current, held := s.current(); held && current.expiresOn.After(s.now) && !s.due(current) {
		return current.id
	}
	return ""
}

func (s workerClientCertificateState) live() []heldWorkerClientCertificate {
	return slices.DeleteFunc(slices.Clone(s.held), func(c heldWorkerClientCertificate) bool { return !c.expiresOn.After(s.now) })
}

func (s workerClientCertificateState) expired() []heldWorkerClientCertificate {
	return slices.DeleteFunc(slices.Clone(s.held), func(c heldWorkerClientCertificate) bool { return c.expiresOn.After(s.now) })
}

func (s workerClientCertificateState) due(current heldWorkerClientCertificate) bool {
	return !s.now.Add(workerClientCertificateRenewal).Before(current.expiresOn)
}

func (s workerClientCertificateState) changes() []edge.PlanChange {
	if !s.enabled {
		return nil
	}
	var changes []edge.PlanChange
	current, held := s.current()
	switch {
	case !held || !current.expiresOn.After(s.now):
		changes = append(changes, edge.PlanChange{Kind: kindMTLSCertificate, Name: s.base, Action: edge.PlanCreate})
	case s.due(current):
		changes = append(changes, edge.PlanChange{
			Kind: kindMTLSCertificate, Name: s.base, Action: edge.PlanUpdate,
			Reason: current.name + " expires " + current.expiresOn.UTC().Format(workerClientCertificateDay) + "; a new one is uploaded beside it",
		})
	default:
		changes = append(changes, edge.PlanChange{Kind: kindMTLSCertificate, Name: s.base, Action: edge.PlanKeep, Reason: current.name + " " + reasonCurrent})
	}
	for _, expired := range s.expired() {
		changes = append(changes, edge.PlanChange{Kind: kindMTLSCertificate, Name: s.base, Action: edge.PlanDelete, Reason: expired.name + " expired"})
	}
	return changes
}

func (s workerClientCertificateState) removals() []edge.PlanChange {
	var changes []edge.PlanChange
	for _, held := range s.held {
		changes = append(changes, edge.PlanChange{Kind: kindMTLSCertificate, Name: held.name, Action: edge.PlanDelete})
	}
	return changes
}

func (c workerClientCertificates) read(ctx context.Context, accountID string, tier environment.Tier, now time.Time) (workerClientCertificateState, error) {
	base, err := accountNameFor("worker client certificate", c.namespace, tier, workerClientCertificateStem)
	if err != nil {
		return workerClientCertificateState{}, err
	}
	state := workerClientCertificateState{enabled: true, base: base, now: now}
	listed := c.client.MTLSCertificates.ListAutoPaging(ctx, mtls_certificates.MTLSCertificateListParams{AccountID: cf.F(accountID)})
	for listed.Next() {
		certificate := listed.Current()
		if certificate.CA || !namesWorkerClientCertificate(certificate.Name, base) {
			continue
		}
		state.held = append(state.held, heldWorkerClientCertificate{
			id: certificate.ID, name: certificate.Name, certificates: certificate.Certificates, expiresOn: certificate.ExpiresOn,
		})
	}
	if err := listed.Err(); err != nil {
		return workerClientCertificateState{}, fmt.Errorf("list the account's mTLS certificates: %w", err)
	}
	slices.SortFunc(state.held, func(a, b heldWorkerClientCertificate) int {
		if c := b.expiresOn.Compare(a.expiresOn); c != 0 {
			return c
		}
		return strings.Compare(a.id, b.id)
	})
	return state, nil
}

func namesWorkerClientCertificate(name, base string) bool {
	date, named := strings.CutPrefix(name, base+"-")
	if !named || len(date) != len(workerClientCertificateDate) {
		return false
	}
	_, err := time.Parse(workerClientCertificateDate, date)
	return err == nil
}

func (c workerClientCertificates) ensure(ctx context.Context, accountID string, state workerClientCertificateState) (edge.Offer, error) {
	for _, expired := range state.expired() {
		_ = c.delete(ctx, accountID, expired.id)
	}
	live := state.live()
	authorities := make([]string, 0, len(live)+1)
	for _, held := range live {
		authority, ok := readEmbeddedCA(held.certificates)
		if !ok {
			return edge.Offer{}, refusal.Refuse(refusal.CodeInvalid,
				"the mTLS certificate %s (%s) is held by the account, and ocel cannot read the CA it chains to, so no origin can be told to trust it: delete it with `wrangler mtls-certificate delete --id %s` and run `ocel bootstrap` again",
				held.id, held.name, held.id)
		}
		authorities = append(authorities, authority)
	}

	var currentID string
	if current, held := state.current(); held && !state.due(current) {
		currentID = current.id
	} else {
		uploaded, authority, err := c.upload(ctx, accountID, state)
		if err != nil {
			return edge.Offer{}, err
		}
		currentID = uploaded
		authorities = slices.Insert(authorities, 0, authority)
	}

	return edge.Offer{
		Kind: edge.OfferWorkerClientCertificate,
		Values: map[string]string{
			edge.OfferKeyClientCertificateID:          currentID,
			edge.OfferKeyClientCertificateAuthorities: strings.Join(slices.Compact(authorities), ""),
		},
	}, nil
}

func (c workerClientCertificates) upload(ctx context.Context, accountID string, state workerClientCertificateState) (id, authority string, err error) {
	leaf, key, err := mintClientCertificate(state.base, state.now)
	if err != nil {
		return "", "", err
	}
	authority, ok := readEmbeddedCA(leaf)
	if !ok {
		return "", "", fmt.Errorf("the worker client certificate ocel minted carries no CA")
	}
	name := state.base + "-" + certificateNotAfter(leaf).UTC().Format(workerClientCertificateDate)
	uploaded, err := c.client.MTLSCertificates.New(ctx, mtls_certificates.MTLSCertificateNewParams{
		AccountID:    cf.F(accountID),
		CA:           cf.F(false),
		Certificates: cf.F(leaf),
		Name:         cf.F(name),
		PrivateKey:   cf.F(key),
	})
	if err != nil {
		return "", "", fmt.Errorf("upload the worker client certificate %q: %w", name, err)
	}
	return uploaded.ID, authority, nil
}

func (c workerClientCertificates) delete(ctx context.Context, accountID, id string) error {
	_, err := c.client.MTLSCertificates.Delete(ctx, id, mtls_certificates.MTLSCertificateDeleteParams{AccountID: cf.F(accountID)})
	if err != nil && !hasStatus(err, http.StatusNotFound) {
		return fmt.Errorf("delete the mTLS certificate %s: %w", id, err)
	}
	return nil
}

func (c workerClientCertificates) teardown(ctx context.Context, accountID string, tier environment.Tier) error {
	state, err := c.read(ctx, accountID, tier, time.Now())
	if err != nil {
		return err
	}
	var errs []error
	for _, held := range state.held {
		if err := c.delete(ctx, accountID, held.id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func certificateNotAfter(certificatePEM string) time.Time {
	block, _ := pem.Decode([]byte(certificatePEM))
	if block == nil {
		return time.Time{}
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}
	}
	return certificate.NotAfter
}
