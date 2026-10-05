// course_content_repo.go — repository selector for the CourseContent
// curriculum aggregate (CHO-1794, L3).
//
// Production wires the pg-backed, RLS-scoped CourseContentRepo (survives pod
// restart + per-tenant isolation); local dev / CHORA_DB_DSN unset falls back to
// the in-memory repo. Mirrors the courseCJ2Port selection in main.go.
package main

import (
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/apollo-chora/chora-delivery/internal/adapter/inmem"
	deliverypg "github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	"github.com/apollo-chora/chora-delivery/internal/domain/course_content"
)

// newCourseContentRepo selects the course-content repository.
//
// The pg-backed, RLS-scoped repo (survives pod restart + per-tenant isolation)
// is used ONLY when BOTH the pool is available AND COURSE_CONTENT_PG_ENABLED is
// truthy. The flag is a migration-safety gate: migration 0029 must be applied
// before the pg path activates, so the code can ship/auto-deploy ahead of the
// migration without breaking course-content reads/writes. Once 0029 is applied
// the flag flips to engage the durable repo (the in-mem fallback is dev-only
// and does NOT survive a redeploy). Mirrors the WEAKNESS_ANALYSER_ENABLED
// migrate-then-activate pattern.
// envBool reports whether the named env var is a truthy flag (1/true/yes/on).
func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func newCourseContentRepo(pool *pgxpool.Pool) course_content.Repository {
	if pgEnabled := envBool("COURSE_CONTENT_PG_ENABLED"); pgEnabled {
		if txr := newPgxTxRunner(pool); txr != nil {
			log.Printf("delivery: course-content repo = pg (RLS-scoped, durable)")
			return deliverypg.NewCourseContentRepo(txr)
		}
		log.Printf("delivery: COURSE_CONTENT_PG_ENABLED set but pool nil — falling back to in-memory course-content repo")
	}
	log.Printf("delivery: course-content repo = in-memory (set COURSE_CONTENT_PG_ENABLED=true after migration 0029 to engage pg)")
	return inmem.NewCourseContentRepo()
}
