package providerhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"
	endpointv1 "github.com/ocelhq/ocel/pkg/proto/endpoint/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/records"
)

var sessionPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

const sweepEvery = time.Minute

type sessionRecord struct {
	Configure []byte    `json:"configure"`
	UsedAt    time.Time `json:"usedAt"`
}

func namedKey(tenant, id string) string { return "named|" + tenant + "|" + id }

func (h *Host) enterNamed(r *http.Request, caller endpoint.Caller, id string, configure bool) (*session, error) {
	named := h.config.Named
	if named == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("this endpoint opens no %s: call it without one", HeaderSession))
	}
	if err := RefuseUnserved(r.Header.Get(HeaderVersion), h.config.Version); err != nil {
		return nil, err
	}
	if !sessionPattern.MatchString(id) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s names no session: 32 lowercase hex digits", HeaderSession))
	}
	tenant := caller.Tenant()
	return h.enter(r.Context(), namedKey(tenant, id), func() (*session, error) {
		mine := func(s *session) bool { return s.named && s.tenant == tenant }
		if h.count(mine) >= named.MaxPerOrganization && !h.evictIdle(mine) {
			return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("the organization has %d provider sessions in a call, the most this endpoint keeps: try again once one ends", named.MaxPerOrganization))
		}
		if err := h.makeRoom(); err != nil {
			return nil, err
		}
		s, err := h.open(caller, namedKey(tenant, id), id, tenant, true, "", "")
		if err != nil {
			return nil, err
		}
		s.tier, s.records, s.cipher = environment.TierProduction, named.Records, named.Cipher
		return s, nil
	}, func(ctx context.Context, s *session) error {
		return h.replay(ctx, s, configure)
	})
}

func RefuseUnserved(version, served string) error {
	if version == served {
		return nil
	}
	refused := connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this endpoint serves ocel %s, and this is ocel %q: run the command with ocel %s", served, version, served))
	if detail, err := connect.NewErrorDetail(&endpointv1.VersionUnserved{Served: []string{served}}); err == nil {
		refused.AddDetail(detail)
	}
	return refused
}

func sessionEnded() error {
	refused := connect.NewError(connect.CodeFailedPrecondition, errors.New("this provider session was never configured or has ended: Configure it again"))
	if detail, err := connect.NewErrorDetail(&endpointv1.SessionEnded{}); err == nil {
		refused.AddDetail(detail)
	}
	return refused
}

func (h *Host) replay(ctx context.Context, s *session, configure bool) error {
	recorded, live, err := h.recorded(ctx, s.tenant, s.id)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("read the provider session's record: %w", err))
	}
	if !live && configure {
		return nil
	}
	if !live {
		return sessionEnded()
	}
	if _, err := h.configureInDirectory(ctx, s, providerserver.DecodeSettings(recorded.GetConfig()), recorded.GetEdge()); err != nil {
		return err
	}
	h.mu.Lock()
	s.configured = true
	h.mu.Unlock()
	return nil
}

func (h *Host) configureNamed(ctx context.Context, s *session, request *connect.Request[contractv1.ConfigureRequest]) (*connect.Response[contractv1.ConfigureResponse], error) {
	facts, err := h.configureInDirectory(ctx, s, providerserver.DecodeSettings(request.Msg.GetConfig()), request.Msg.GetEdge())
	if err != nil {
		return nil, err
	}
	if err := h.record(ctx, s, request.Msg); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("record the provider session: %w", err))
	}
	h.mu.Lock()
	s.configured = true
	h.mu.Unlock()
	return connect.NewResponse(&contractv1.ConfigureResponse{Facts: facts}), nil
}

func (h *Host) record(ctx context.Context, s *session, request *contractv1.ConfigureRequest) error {
	msg := proto.CloneOf(request)
	if msg.Config != nil {
		msg.Config.ProjectDir = ""
	}
	plaintext, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	named := h.config.Named
	sealed, err := named.Cipher.Seal(ctx, environment.TierProduction, providerserver.SessionAssociatedData(s.tenant, s.id), plaintext)
	if err != nil {
		return err
	}
	value, err := json.Marshal(sessionRecord{Configure: sealed, UsedAt: h.config.Now()})
	if err != nil {
		return err
	}
	return records.Change(ctx, named.Records, providerserver.SessionKey(s.tenant, s.id), func(records.Entry) ([]byte, bool, error) {
		return value, true, nil
	})
}

func (h *Host) recorded(ctx context.Context, tenant, id string) (*contractv1.ConfigureRequest, bool, error) {
	named := h.config.Named
	key := providerserver.SessionKey(tenant, id)
	entry, err := named.Records.Read(ctx, key)
	if errors.Is(err, records.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	recorded, err := readSession(entry)
	if err != nil {
		return nil, false, err
	}
	if h.ended(recorded) {
		return nil, false, records.ForgetMatching(ctx, named.Records, key, h.endedEntry)
	}
	plaintext, err := named.Cipher.Open(ctx, environment.TierProduction, providerserver.SessionAssociatedData(tenant, id), recorded.Configure)
	if err != nil {
		return nil, false, err
	}
	request := &contractv1.ConfigureRequest{}
	if err := proto.Unmarshal(plaintext, request); err != nil {
		return nil, false, fmt.Errorf("read the session's Configure: %w", err)
	}
	return request, true, nil
}

func readSession(entry records.Entry) (sessionRecord, error) {
	var recorded sessionRecord
	if err := json.Unmarshal(entry.Value, &recorded); err != nil {
		return sessionRecord{}, fmt.Errorf("read %s: %w", entry.Key, err)
	}
	return recorded, nil
}

func (h *Host) ended(recorded sessionRecord) bool {
	return h.config.Now().Sub(recorded.UsedAt) >= h.config.IdleAfter
}

func (h *Host) endedEntry(entry records.Entry) (bool, error) {
	recorded, err := readSession(entry)
	return err == nil && h.ended(recorded), nil
}

func (h *Host) touch(ctx context.Context, s *session) error {
	err := records.Change(ctx, h.config.Named.Records, providerserver.SessionKey(s.tenant, s.id), func(entry records.Entry) ([]byte, bool, error) {
		if len(entry.Value) == 0 {
			return nil, false, nil
		}
		recorded, err := readSession(entry)
		if err != nil {
			return nil, false, err
		}
		recorded.UsedAt = h.config.Now()
		value, err := json.Marshal(recorded)
		return value, err == nil, err
	})
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("record the provider session's use: %w", err))
	}
	return nil
}

func (h *Host) sweepRecords(ctx context.Context) {
	named := h.config.Named
	if named == nil {
		return
	}
	if sessions, err := named.Records.List(ctx, providerserver.SessionsPartition()); err == nil {
		for _, entry := range sessions {
			if ended, _ := h.endedEntry(entry); ended {
				_ = records.ForgetMatching(ctx, named.Records, entry.Key, h.endedEntry)
			}
		}
	}
	if asked, err := named.Records.List(ctx, providerserver.QuestionsPartition(environment.TierProduction)); err == nil {
		for _, entry := range asked {
			if abandoned, _ := h.abandonedEntry(entry); abandoned {
				_ = records.ForgetMatching(ctx, named.Records, entry.Key, h.abandonedEntry)
			}
		}
	}
}
