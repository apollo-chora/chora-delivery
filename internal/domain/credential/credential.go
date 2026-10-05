// Package credential owns the operator-curated Credential aggregate — a
// catalogue entity (e.g. PMP) defined by operators / awarding bodies with a
// structured breakdown of Competencies. Per ADR-216 D1/D2 (Learner-Sovereign
// Discovery redesign, WS-1).
//
// Scope (WS-1 only): the Credential-with-competencies aggregate + a
// tenant-scoped catalogue read. This is the thing a later learner aspiration
// link (WS-3) points at, and the thing gap analysis (WS-4) diffs against; the
// crosswalk (WS-2), aspiration link, gap analysis and RPL are LATER
// workstreams and are NOT built here.
//
// D1 — operator-curated, learner-immutable. Immutability-by-learners is an API
// property: only operator roles (instructor / admin / training-admin) may
// create a Credential, and no learner-facing write path exists — the learner
// only ever READS the catalogue. The aggregate carries no learner-writable
// state.
//
// D2 — a Competency's Code is the seed of the shared competency vocabulary: a
// stable, normalised key that a learner's ConceptNode will later map onto (the
// WS-2 crosswalk). The credential's competency list itself stays operator-owned.
//
// Modelling choice: Competencies are a CHILD COLLECTION persisted INSIDE the
// Credential's JSONB snapshot (the delivery.Offering.Sections idiom) — they have
// no independent lifecycle, are reached only through the Credential root, own no
// table and carry no FK. This keeps a credential's structure lossless + a single
// aggregate write, and matches how chora-delivery models similar structured
// aggregates.
//
// Cross-domain references (none here) would travel as opaque UUIDs without FK
// per .claude/rules/ddd-enforcement.md invariant #3. This is a CATALOGUE entity
// only: it does NOT touch the opaque issued-cert model (internal/domain/
// certification) — existing issued certificates are unaffected.
//
// Hexagonal: pure domain — NO HTTP, NO persistence imports. UUIDv7 IDs
// (sortable + time-correlated); soft-delete via DeletedAt (NEVER hard-delete).
package credential

import (
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
)

// -----------------------------------------------------------------------------
// Errors (sentinels — handlers map these to 400)
// -----------------------------------------------------------------------------

var (
	// ErrTenantRequired — tenant_id must be trim-non-empty.
	ErrTenantRequired = errors.New("credential: tenant_id required")
	// ErrTitleRequired — a credential must carry a human title.
	ErrTitleRequired = errors.New("credential: title required")
	// ErrCompetenciesRequired — a credential must carry at least one competency.
	// A competency-less credential is just the opaque cert model again (there is
	// nothing structured to diff against — ADR-216 rejects that).
	ErrCompetenciesRequired = errors.New("credential: at least one competency required")
	// ErrCompetencyNameRequired — every competency needs a human label.
	ErrCompetencyNameRequired = errors.New("credential: competency name required")
	// ErrCompetencyCodeRequired — every competency needs a shared-vocabulary code.
	ErrCompetencyCodeRequired = errors.New("credential: competency code required")
	// ErrDuplicateCompetencyCode — competency codes must be unique within a
	// credential (compared after normalisation).
	ErrDuplicateCompetencyCode = errors.New("credential: duplicate competency code")
	// ErrCompetencyWeightNegative — a competency weight must be ≥ 0
	// (0 = unspecified).
	ErrCompetencyWeightNegative = errors.New("credential: competency weight must be ≥ 0 (0 = unspecified)")
)

// -----------------------------------------------------------------------------
// Aggregate
// -----------------------------------------------------------------------------

// Credential is the operator-curated catalogue aggregate root: a named
// credential defined by a structured set of Competencies.
//
// Every exported field round-trips through json.Marshal losslessly — the pg
// adapter stores the whole struct as a JSONB snapshot (the offering_sessions /
// offerings pattern). Structs carry NO json tags, so JSONB keys are the
// PascalCase field names.
type Credential struct {
	ID          string
	TenantID    string
	Title       string // human name, e.g. "Project Management Professional"
	Code        string // operator short code, e.g. "PMP" (optional)
	IssuingBody string // awarding body, e.g. "PMI" (optional)
	Description string // optional
	// Competencies is the structured breakdown (domains / tasks). A child
	// collection reached only via this root and persisted inside the JSONB
	// aggregate (no own table, no FK). Non-empty by construction.
	Competencies []Competency
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DeletedAt    *time.Time // soft-delete tombstone (Archive); NEVER hard-delete
}

// Competency is one structured skill/domain/task in a Credential's breakdown.
// It is a child entity of the Credential aggregate — no independent lifecycle,
// no own table, no cross-aggregate references.
type Competency struct {
	// CompetencyID is a stable UUIDv7 the later crosswalk (WS-2) + gap analysis
	// (WS-4) reference.
	CompetencyID string
	// Code is the shared-vocabulary key (normalised: trimmed, lowercased,
	// internal whitespace hyphenated) — the D2 seed a learner ConceptNode maps
	// onto. Unique within the credential.
	Code string
	// Name is the human label, e.g. "People" (keeps its authored casing).
	Name string
	// Description is optional prose.
	Description string
	// Weight is an optional relative weight / exam percentage (0 = unspecified).
	Weight int
}

// -----------------------------------------------------------------------------
// Construction
// -----------------------------------------------------------------------------

// CompetencyInput is the value-bag for one competency in NewCredentialInput.
type CompetencyInput struct {
	Code        string
	Name        string
	Description string
	Weight      int
}

// NewCredentialInput is the value-bag for NewCredential.
type NewCredentialInput struct {
	TenantID     string
	Title        string
	Code         string
	IssuingBody  string
	Description  string
	Competencies []CompetencyInput
}

// NewCredential constructs a Credential with all guards.
//
// Validation:
//   - tenant_id + title trim-non-empty (ErrTenantRequired / ErrTitleRequired);
//   - at least one competency (ErrCompetenciesRequired);
//   - every competency: name + code trim-non-empty, weight ≥ 0;
//   - competency codes unique within the credential (post-normalisation).
//
// Each competency gets a fresh UUIDv7; its Code is normalised into the shared
// vocabulary key. Header fields are trimmed (Code/IssuingBody/Description are
// optional). The credential + every competency share a single `now`.
func NewCredential(in NewCredentialInput) (*Credential, error) {
	tenant := strings.TrimSpace(in.TenantID)
	if tenant == "" {
		return nil, ErrTenantRequired
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, ErrTitleRequired
	}
	if len(in.Competencies) == 0 {
		return nil, ErrCompetenciesRequired
	}
	now := time.Now().UTC()
	comps := make([]Competency, 0, len(in.Competencies))
	seen := make(map[string]bool, len(in.Competencies))
	for _, ci := range in.Competencies {
		name := strings.TrimSpace(ci.Name)
		if name == "" {
			return nil, ErrCompetencyNameRequired
		}
		code := normaliseCode(ci.Code)
		if code == "" {
			return nil, ErrCompetencyCodeRequired
		}
		if seen[code] {
			return nil, fmt.Errorf("%w: %q", ErrDuplicateCompetencyCode, code)
		}
		if ci.Weight < 0 {
			return nil, ErrCompetencyWeightNegative
		}
		seen[code] = true
		comps = append(comps, Competency{
			CompetencyID: NewUUIDv7(),
			Code:         code,
			Name:         name,
			Description:  strings.TrimSpace(ci.Description),
			Weight:       ci.Weight,
		})
	}
	return &Credential{
		ID:           NewUUIDv7(),
		TenantID:     tenant,
		Title:        title,
		Code:         strings.TrimSpace(in.Code),
		IssuingBody:  strings.TrimSpace(in.IssuingBody),
		Description:  strings.TrimSpace(in.Description),
		Competencies: comps,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// -----------------------------------------------------------------------------
// Lifecycle
// -----------------------------------------------------------------------------

// Archive soft-deletes the credential (sets DeletedAt + bumps UpdatedAt). NEVER
// hard-delete. Idempotent — a second call on an already-archived credential is a
// no-op (the tombstone does not move).
func (c *Credential) Archive() {
	if c.DeletedAt != nil {
		return
	}
	now := time.Now().UTC()
	c.DeletedAt = &now
	c.UpdatedAt = now
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

// normaliseCode reduces a competency code to its shared-vocabulary key: trimmed,
// lowercased, with runs of internal whitespace collapsed to a single hyphen.
// Case- + whitespace-insensitive so "Risk Management" and "risk-management"
// resolve to the same vocabulary entry. Returns "" for a blank input.
func normaliseCode(raw string) string {
	fields := strings.Fields(strings.ToLower(raw))
	return strings.Join(fields, "-")
}

// NewUUIDv7 returns a freshly generated UUIDv7 string per RFC 9562 §5.7. Local
// copy keeps the package dependency-free (mirrors offering_session.NewUUIDv7).
func NewUUIDv7() string {
	const buflen = 16
	var b [buflen]byte
	now := uint64(time.Now().UnixMilli())
	b[0] = byte(now >> 40)
	b[1] = byte(now >> 32)
	b[2] = byte(now >> 24)
	b[3] = byte(now >> 16)
	b[4] = byte(now >> 8)
	b[5] = byte(now)
	if _, err := rand.Read(b[6:]); err != nil {
		for i := 6; i < buflen; i++ {
			b[i] = byte(now >> uint(8*(i-6)))
		}
	}
	b[6] = (b[6] & 0x0F) | 0x70
	b[8] = (b[8] & 0x3F) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
