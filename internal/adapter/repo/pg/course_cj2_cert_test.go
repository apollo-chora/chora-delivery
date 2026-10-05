// course_cj2_cert_test.go — cert-definition persistence (CHO-1795, migration
// 0030). Verifies SaveCJ2 runs the extra cert UPDATE + GetCJ2 round-trips the
// cert columns ONLY when EnableCertColumns is set (the migration-safety gate).
package pg_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	domain "github.com/apollo-chora/chora-delivery/internal/domain/delivery"
)

func certTestCourse(t *testing.T) *domain.Course {
	t.Helper()
	c, err := domain.NewCJ2Course(domain.NewCJ2CourseInput{
		TenantID: tenantID, AuthorGCID: "00000000-0000-7000-8000-000000001999",
		Title: "Cert course", LearningObjectives: []string{"LO1"},
		TestSetIDs:    []string{"01970000-0000-7000-8000-0000000000aa"},
		Certification: &domain.CertDefinition{Enabled: true, CertType: domain.CertTypeCompetency, PassingScorePct: 75, RequireAllContent: true},
	})
	if err != nil {
		t.Fatalf("NewCJ2Course: %v", err)
	}
	return c
}

func TestSaveCJ2_WithCertColumns_RunsCertUpdate(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCourseRepo(&stubTxRunner{q: q}).EnableCertColumns()
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.SaveCJ2(ctx, certTestCourse(t)); err != nil {
		t.Fatalf("SaveCJ2: %v", err)
	}
	// Find the cert UPDATE statement.
	idx := -1
	for i, s := range q.sqls {
		if strings.Contains(s, "cert_enabled") && strings.Contains(s, "UPDATE courses") {
			idx = i
		}
	}
	if idx == -1 {
		t.Fatalf("expected a cert UPDATE; sqls=%v", q.sqls)
	}
	args := q.args[idx]
	if en, _ := args[0].(bool); !en {
		t.Fatalf("cert_enabled arg should be true, got %v", args[0])
	}
	if ct, _ := args[1].(string); ct != "COMPETENCY" {
		t.Fatalf("cert_type arg should be COMPETENCY, got %v", args[1])
	}
	if ps, _ := args[2].(int); ps != 75 {
		t.Fatalf("passing_score arg should be 75, got %v", args[2])
	}
}

func TestSaveCJ2_WithoutCertColumns_SkipsCertUpdate(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewCourseRepo(&stubTxRunner{q: q}) // cert columns NOT enabled
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	if err := r.SaveCJ2(ctx, certTestCourse(t)); err != nil {
		t.Fatalf("SaveCJ2: %v", err)
	}
	for _, s := range q.sqls {
		if strings.Contains(s, "cert_enabled") {
			t.Fatalf("cert UPDATE must be skipped when columns disabled; sqls=%v", q.sqls)
		}
	}
}

func TestGetCJ2_WithCertColumns_RoundTripsCert(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	q := &stubQuerier{
		rowFn: func(sql string, args ...any) pg.Row {
			if strings.Contains(sql, "cert_enabled") {
				// cert SELECT: enabled, cert_type, passing_score, require_all
				return stubRow{scanFn: func(dest ...any) error {
					*(dest[0].(*bool)) = true
					ct := "ACCREDITED"
					*(dest[1].(**string)) = &ct
					ps := 90
					*(dest[2].(**int)) = &ps
					*(dest[3].(*bool)) = false
					return nil
				}}
			}
			// main CJ2 course SELECT (22 cols) — fill the non-null required ones.
			return stubRow{scanFn: func(dest ...any) error {
				*(dest[0].(*string)) = "01970000-0000-7000-8000-0000000000c1" // id
				*(dest[1].(*string)) = tenantID                               // tenant_id
				*(dest[2].(*string)) = "00000000-0000-7000-8000-000000001999" // instructor_gcid
				*(dest[3].(*string)) = "Cert course"                          // title
				*(dest[4].(*string)) = "desc"                                 // description
				*(dest[6].(*bool)) = true                                     // public
				*(dest[7].(*int64)) = 0                                       // price_sgd_cents
				*(dest[8].(*bool)) = false                                    // sf_eligible
				*(dest[9].(*int)) = 1                                         // max_capacity
				*(dest[10].(*time.Time)) = now                                // created_at
				*(dest[11].(*time.Time)) = now                                // updated_at
				*(dest[13].(*string)) = "PUBLISHED"                           // state
				return nil
			}}
		},
	}
	r := pg.NewCourseRepo(&stubTxRunner{q: q}).EnableCertColumns()
	port := &pg.CourseRepoCJ2Port{Repo: r}
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	c, ok, err := port.Get(ctx, tenantID, "01970000-0000-7000-8000-0000000000c1")
	if err != nil || !ok || c == nil {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if !c.Certification.Enabled || c.Certification.CertType != domain.CertTypeAccredited || c.Certification.PassingScorePct != 90 || c.Certification.RequireAllContent {
		t.Fatalf("cert not round-tripped: %+v", c.Certification)
	}
}
