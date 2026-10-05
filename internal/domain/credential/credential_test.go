package credential

import (
	"errors"
	"testing"
)

func validCredentialInput() NewCredentialInput {
	return NewCredentialInput{
		TenantID:    "11111111-1111-7111-8111-111111111111",
		Title:       "Project Management Professional",
		Code:        "PMP",
		IssuingBody: "PMI",
		Description: "Globally recognised project-management credential.",
		Competencies: []CompetencyInput{
			{Code: "People", Name: "People", Description: "Leading the team", Weight: 42},
			{Code: "Process", Name: "Process", Description: "Managing the technical aspects", Weight: 50},
			{Code: "Business Environment", Name: "Business Environment", Weight: 8},
		},
	}
}

func TestNewCredential_OK(t *testing.T) {
	c, err := NewCredential(validCredentialInput())
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	if c.ID == "" {
		t.Fatal("ID not generated")
	}
	if c.Title != "Project Management Professional" || c.Code != "PMP" || c.IssuingBody != "PMI" {
		t.Fatalf("header fields: got title=%q code=%q body=%q", c.Title, c.Code, c.IssuingBody)
	}
	if len(c.Competencies) != 3 {
		t.Fatalf("want 3 competencies; got %d", len(c.Competencies))
	}
	if c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		t.Fatal("timestamps not stamped")
	}
	if c.DeletedAt != nil {
		t.Fatal("DeletedAt should be nil on a fresh credential")
	}
	// Each competency gets a stable UUIDv7 id.
	seen := map[string]bool{}
	for _, cp := range c.Competencies {
		if cp.CompetencyID == "" {
			t.Fatalf("competency %q missing generated id", cp.Name)
		}
		if seen[cp.CompetencyID] {
			t.Fatalf("competency ids must be unique; dup %q", cp.CompetencyID)
		}
		seen[cp.CompetencyID] = true
	}
}

func TestNewCredential_NormalisesCompetencyCode(t *testing.T) {
	in := validCredentialInput()
	in.Competencies = []CompetencyInput{
		{Code: "  Risk Management  ", Name: "Risk Management"},
	}
	c, err := NewCredential(in)
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	// Code is trimmed + lowercased + internal whitespace hyphenated → a stable
	// shared-vocabulary key. Name keeps its human casing.
	if c.Competencies[0].Code != "risk-management" {
		t.Fatalf("code normalisation: got %q, want %q", c.Competencies[0].Code, "risk-management")
	}
	if c.Competencies[0].Name != "Risk Management" {
		t.Fatalf("name should keep casing; got %q", c.Competencies[0].Name)
	}
}

func TestNewCredential_TrimsHeaderFields(t *testing.T) {
	in := validCredentialInput()
	in.Title = "  PMP Cert  "
	in.Code = "  pmp  "
	in.IssuingBody = "  PMI  "
	c, err := NewCredential(in)
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	if c.Title != "PMP Cert" || c.Code != "pmp" || c.IssuingBody != "PMI" {
		t.Fatalf("trims: got title=%q code=%q body=%q", c.Title, c.Code, c.IssuingBody)
	}
}

func TestNewCredential_Guards(t *testing.T) {
	cases := map[string]struct {
		mutate func(*NewCredentialInput)
		want   error
	}{
		"missing tenant": {
			func(in *NewCredentialInput) { in.TenantID = "  " },
			ErrTenantRequired,
		},
		"missing title": {
			func(in *NewCredentialInput) { in.Title = "" },
			ErrTitleRequired,
		},
		"no competencies": {
			func(in *NewCredentialInput) { in.Competencies = nil },
			ErrCompetenciesRequired,
		},
		"competency missing name": {
			func(in *NewCredentialInput) { in.Competencies[0].Name = "  " },
			ErrCompetencyNameRequired,
		},
		"competency missing code": {
			func(in *NewCredentialInput) { in.Competencies[0].Code = "" },
			ErrCompetencyCodeRequired,
		},
		"duplicate competency code": {
			func(in *NewCredentialInput) { in.Competencies[1].Code = in.Competencies[0].Code },
			ErrDuplicateCompetencyCode,
		},
		"duplicate competency code after normalisation": {
			func(in *NewCredentialInput) { in.Competencies[1].Code = "  PEOPLE " },
			ErrDuplicateCompetencyCode,
		},
		"negative weight": {
			func(in *NewCredentialInput) { in.Competencies[0].Weight = -1 },
			ErrCompetencyWeightNegative,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := validCredentialInput()
			tc.mutate(&in)
			_, err := NewCredential(in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestCredential_Archive_SoftDeletesIdempotently(t *testing.T) {
	c, err := NewCredential(validCredentialInput())
	if err != nil {
		t.Fatalf("NewCredential: %v", err)
	}
	before := c.UpdatedAt
	c.Archive()
	if c.DeletedAt == nil {
		t.Fatal("Archive must set DeletedAt (soft-delete, never hard-delete)")
	}
	if !c.UpdatedAt.After(before) && !c.UpdatedAt.Equal(*c.DeletedAt) {
		t.Fatal("Archive must bump UpdatedAt")
	}
	first := *c.DeletedAt
	c.Archive() // idempotent — a second call must not move the tombstone
	if !c.DeletedAt.Equal(first) {
		t.Fatalf("Archive must be idempotent; DeletedAt moved %v -> %v", first, *c.DeletedAt)
	}
}
