// Command backfill-course-directory re-emits chora.delivery.course.released.v1
// for courses that predate the course_directory projection lane (CHO-2247).
//
// ⚠ OPERATOR TOOL — NOT WIRED INTO ANY SERVICE. It is never started by
// cmd/server and never runs on a schedule. It is run by hand, once, by the
// platform owner, per docs/runbooks/cho-2247-course-directory-backfill.md, AFTER
// the consuming subscription exists.
//
// WHY A RE-EMIT AND NOT A BACKFILL JOB
// ------------------------------------
// course_directory lives in chora_consumption. Writing it from here would be a
// cross-domain write straight through the Pub/Sub boundary — .claude/rules/
// ddd-enforcement.md HARD RULE #1, and a NEW breach class beyond the two
// ADR-scoped RLS-bypass surfaces (ADR-165 / ADR-184). So this tool does the one
// thing delivery is entitled to do: read its OWN database and emit its OWN
// event. chora-consumption projects it, exactly as it does for a live release.
//
// WHY THERE IS NOTHING TO "REPLAY"
// --------------------------------
// You cannot re-emit what was never emitted. The delivery outbox holds no
// course.created rows for 17 of 19 courses, and Pub/Sub's 7-day retention has
// already expired 8 of the 9 historical course.released events. So this tool
// SYNTHESISES fresh events from courses (the system of record) rather than
// replaying history.
//
// SAFETY POSTURE
//   - Default is --dry-run. Emitting requires --emit explicitly.
//   - Emits are TEED THROUGH THE OUTBOX, not published directly: the running
//     chora-delivery dispatcher performs the actual publish, so these events get
//     the identical schema validation, attrs["topic"] stamp, retry, and DLQ
//     posture as a live release. This tool owns no publish path of its own.
//   - Idempotent by construction downstream: the consumption projection dedupes
//     on event_id and upserts last-writer-wins on updated_at
//     (WHERE course_directory.updated_at < EXCLUDED.updated_at), so a re-run
//     cannot clobber a newer title.
//   - occurred_at is the course's OWN published_at/updated_at, never now(). A
//     synthetic now() would make every backfilled row the newest writer and
//     would clobber a real title that arrived while the sweep was running.
//   - --canary-verified is MANDATORY for a sweep. See below.
//
// GUARD 3 — CANARY BEFORE SWEEP, ENFORCED RATHER THAN ADVISED
// -----------------------------------------------------------
// The course.created → course_directory lane has NEVER projected a production
// row: both course.created events ever emitted published successfully and
// neither course reached the directory. A green publish is NOT a projected row.
// So this tool cannot sweep until a canary has demonstrably landed:
//
//	step 1: --canary            emits exactly ONE event, then prints the SQL to
//	                            verify the row (run it yourself against
//	                            chora_consumption).
//	step 2: --emit --canary-verified=<course_id>
//	                            refuses unless the id names a course this tool
//	                            actually canaried.
//
// The verification is deliberately the OPERATOR's step, not the tool's: this
// binary lives in chora-delivery and MUST NOT read chora_consumption. A tool
// that verified its own canary by querying the other domain's database would be
// the exact cross-DB violation this design exists to avoid.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/apollo-chora/chora-delivery/internal/adapter/events"
	"github.com/apollo-chora/chora-delivery/internal/adapter/outbox"

	"github.com/apollo-chora/chora-common/env"
)

const topicCourseReleased = "chora.delivery.course.released.v1"

// sqlSelectPublishedCourses reads the courses the sweep will re-emit.
//
// ⚠ EVERY uuid column is cast to ::text. courses.course_id, .tenant_id and
// .author_gcid are all `uuid` in Postgres, and this tool scans them into Go
// strings. The casts are load-bearing in two distinct ways:
//
//   - COALESCE(author_gcid, ”) without the cast coalesces a uuid with an empty
//     STRING, so Postgres tries ”::uuid and the whole query dies at parse time
//     with 22P02 "invalid input syntax for type uuid" — on EVERY tenant,
//     unconditionally. That was the shipped bug (CHO-2247): a stub Querier in the
//     unit suite never type-checks SQL, so it was green while the tool could not
//     read a single row.
//   - the ::text casts on course_id/tenant_id keep the driver's scan into
//     `string` explicit rather than relying on the pgx uuid codec's string
//     support, so a driver/codec change cannot silently reintroduce a scan error.
//
// Exported as a const so main_integration_test.go can PREPARE it against live
// Postgres. A prepare-smoke type-checks the statement server-side WITHOUT
// executing it — the only thing that could have caught this class of bug.
const sqlSelectPublishedCourses = `
SELECT course_id::text, tenant_id::text, title, COALESCE(author_gcid::text, ''), state,
       COALESCE(published_at, updated_at, created_at)
  FROM courses
 WHERE deleted_at IS NULL AND state = 'PUBLISHED'
 ORDER BY created_at`

// sqlDBAdapter bridges *sql.DB to outbox.SQLDB (whose QueryContext returns the
// package's own SQLRows so tests can stub it). Mirrors the identical adapter in
// cmd/server/bootstrap.go, which is unexported and therefore not importable
// here. Kept byte-for-byte equivalent on purpose: this tool tees into the SAME
// outbox table the server does, and a divergent adapter would be a second,
// subtly-different write path into it.
type sqlDBAdapter struct{ db *sql.DB }

func (a sqlDBAdapter) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return a.db.ExecContext(ctx, query, args...)
}

func (a sqlDBAdapter) QueryContext(ctx context.Context, query string, args ...any) (outbox.SQLRows, error) {
	return a.db.QueryContext(ctx, query, args...)
}

var _ outbox.SQLDB = sqlDBAdapter{}

type courseRow struct {
	CourseID   string
	TenantID   string
	Title      string
	AuthorGCID string
	OccurredAt time.Time
	State      string
}

func main() {
	log.SetFlags(0)
	var (
		emit        = flag.Bool("emit", false, "actually write events to the outbox (default: dry-run)")
		canary      = flag.Bool("canary", false, "emit exactly ONE event and print the verification SQL")
		verified    = flag.String("canary-verified", "", "course_id of a canary you have CONFIRMED landed in course_directory")
		tenantsFlag = flag.String("tenants", "", "comma-separated tenant_ids to sweep (required — no implicit all-tenants)")
	)
	flag.Parse()

	dsn := strings.TrimSpace(os.Getenv("CHORA_DELIVERY_DSN"))
	if dsn == "" {
		fatal("CHORA_DELIVERY_DSN is empty — no inline config (feedback_no_inline_config). Source it from Secret Manager: chora-dev-cloudsql-chora_delivery-app_rw-dsn")
	}
	tenants := splitNonEmpty(*tenantsFlag)
	if len(tenants) == 0 {
		fatal("--tenants is required. There is no implicit all-tenants sweep: chora_delivery.courses is FORCE-RLS and a query with no chora.tenant_id set returns rows only for the policy's public arm — a silent PARTIAL sweep that would look like a complete one.")
	}
	if *canary && *emit {
		fatal("--canary and --emit are mutually exclusive: canary emits one event, emit sweeps the rest")
	}
	if *emit && strings.TrimSpace(*verified) == "" {
		fatal("refusing to sweep without --canary-verified=<course_id>.\n" +
			"  The created→directory lane has never projected a production row; a green publish is not a projected row.\n" +
			"  Run --canary first, verify the row landed in chora_consumption, then pass that course_id here.")
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		fatal("open chora_delivery: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		fatal("ping chora_delivery: %v", err)
	}

	rows, err := loadCourses(ctx, db, tenants)
	if err != nil {
		fatal("load courses: %v", err)
	}
	if len(rows) == 0 {
		fatal("0 courses read across %d tenant(s). That is almost certainly an RLS false negative, not an empty platform — verify chora.tenant_id is being set and the tenant_ids are real.", len(tenants))
	}
	log.Printf("read %d PUBLISHED course(s) across %d tenant(s)", len(rows), len(tenants))

	if *emit && !containsCourse(rows, *verified) {
		fatal("--canary-verified=%s names a course this sweep did not read. Pass the id you actually canaried.", *verified)
	}

	switch {
	case *canary:
		runCanary(ctx, db, rows)
	case *emit:
		runSweep(ctx, db, rows, *verified)
	default:
		runDryRun(rows)
	}
}

func runDryRun(rows []courseRow) {
	log.Printf("DRY RUN — nothing written. %d event(s) would be teed into outbox_events:", len(rows))
	for _, c := range rows {
		log.Printf("  %s  occurred_at=%s  %q", c.CourseID, c.OccurredAt.UTC().Format(time.RFC3339), c.Title)
	}
	log.Printf("\nnext: --canary (one event), verify it landed, then --emit --canary-verified=<id>")
}

func runCanary(ctx context.Context, db *sql.DB, rows []courseRow) {
	c := rows[0]
	log.Printf("CANARY — emitting exactly 1 event for %s (%q)", c.CourseID, c.Title)
	if err := emitOne(ctx, db, c); err != nil {
		fatal("canary emit failed: %v", err)
	}
	log.Printf("canary teed into outbox_events. The dispatcher publishes it within its poll interval.\n")
	log.Printf("VERIFY IT LANDED — run against chora_consumption (NOT this tool: delivery must never read consumption):\n\n"+
		"  SELECT course_id, title, updated_at FROM course_directory WHERE course_id = '%s';\n\n"+
		"A row with the real title ⇒ the lane works end to end. Then:\n\n"+
		"  go run ./cmd/backfill-course-directory --emit --canary-verified=%s --tenants=<...>\n\n"+
		"NO row ⇒ STOP. The lane is broken somewhere between publish and projection\n"+
		"(subscription? per-subscription IAM? Istio authz on /api/internal/pubsub/course-metadata?\n"+
		"protodecode registration?). Do NOT sweep into a broken lane.\n", c.CourseID, c.CourseID)
}

func runSweep(ctx context.Context, db *sql.DB, rows []courseRow, verifiedID string) {
	log.Printf("SWEEP — canary %s confirmed landed; emitting the remaining %d course(s)", verifiedID, len(rows)-1)
	var emitted, failed int
	for _, c := range rows {
		if c.CourseID == verifiedID {
			continue // already emitted + verified as the canary
		}
		if err := emitOne(ctx, db, c); err != nil {
			// Fail LOUD and keep going: one bad row must not silently abort a
			// sweep, and it must not silently vanish either.
			log.Printf("  FAILED %s (%q): %v", c.CourseID, c.Title, err)
			failed++
			continue
		}
		emitted++
	}
	log.Printf("sweep complete: %d emitted, %d FAILED", emitted, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

// emitOne tees a single course.released.v1 into outbox_events through the SAME
// publisher chain the service uses. occurred_at is the course's own timestamp,
// which the consumption projection uses as its last-writer-wins clock.
func emitOne(ctx context.Context, db *sql.DB, c courseRow) error {
	inner := events.NewInMemoryPublisher(env.GetOrDefault("CHORA_SOURCE_PROJECT", "chora-489812"), "chora-delivery-backfill")
	store := outbox.NewPostgresStore(sqlDBAdapter{db: db}, outbox.PostgresStoreOptions{WorkerID: "cho2247-backfill"})
	pub := outbox.NewTransactionalPublisher(outbox.PublisherConfig{Inner: inner, Store: store})

	payload := map[string]any{
		"course_id":   c.CourseID,
		"title":       c.Title,
		"author_gcid": c.AuthorGCID,
		"released_at": c.OccurredAt.UTC(),
	}
	pc, ok := any(pub).(interface {
		PublishCustom(topic, tenantID, gcid string, payload map[string]any) (events.PublishedEvent, error)
	})
	if !ok {
		return errors.New("outbox publisher does not implement PublishCustom")
	}
	_ = ctx
	if _, err := pc.PublishCustom(topicCourseReleased, c.TenantID, c.AuthorGCID, payload); err != nil {
		return fmt.Errorf("tee course.released for %s: %w", c.CourseID, err)
	}
	return nil
}

// loadCourses reads PUBLISHED, non-deleted courses per tenant.
//
// ⚠ The chora.tenant_id SET is not optional. courses is FORCE-RLS
// (relforcerowsecurity=t) and no available role carries BYPASSRLS — only
// cloudsqladmin, which is Google-managed. Without the GUC the policy falls to its
// public arm and returns a partial set that looks exactly like a complete one.
// chora.user_gcid is deliberately left UNSET: the policy's first arm then admits
// every state for the tenant rather than only PUBLISHED-or-authored rows.
func loadCourses(ctx context.Context, db *sql.DB, tenants []string) ([]courseRow, error) {
	var out []courseRow
	for _, t := range tenants {
		conn, err := db.Conn(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := conn.ExecContext(ctx, "SELECT set_config('chora.tenant_id', $1, false)", t); err != nil {
			conn.Close()
			return nil, fmt.Errorf("set chora.tenant_id=%s: %w", t, err)
		}
		rs, err := conn.QueryContext(ctx, sqlSelectPublishedCourses)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("query courses for %s: %w", t, err)
		}
		for rs.Next() {
			var c courseRow
			if err := rs.Scan(&c.CourseID, &c.TenantID, &c.Title, &c.AuthorGCID, &c.State, &c.OccurredAt); err != nil {
				rs.Close()
				conn.Close()
				return nil, err
			}
			out = append(out, c)
		}
		err = rs.Err()
		rs.Close()
		conn.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func containsCourse(rows []courseRow, id string) bool {
	for _, c := range rows {
		if c.CourseID == id {
			return true
		}
	}
	return false
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func fatal(format string, args ...any) {
	log.Printf("FATAL: "+format, args...)
	os.Exit(2)
}
