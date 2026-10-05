//go:build integration

// offering_session_double_book_integration_test.go — proves ratified CHO-2191 /
// ADR-237's room_id double-book gate against LIVE Cloud SQL. The unit smoke
// asserts the SQL surface; only a real Postgres exercises the GiST EXCLUDE
// constraint (mig 0052) that rejects two overlapping same-room_id sessions.
// Requires mig 0052 applied.
//
//	export GOOGLE_APPLICATION_CREDENTIALS=$HOME/.config/gcloud/sa-keys/dale-cli-chora-489812.json
//	export CHORA_TEST_DSN_SECRET_ID=chora-dev-cloudsql-chora_delivery-app_rw-dsn
//	export CHORA_TEST_DB_PROJECT=chora-489812
//	go test -tags integration -run TestIntegration_OfferingSessionRepo_RoomDoubleBook \
//	  ./services/chora-delivery/internal/adapter/repo/pg/...
package pg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	offeringsession "github.com/apollo-chora/chora-delivery/internal/domain/offering_session"
)

func TestIntegration_OfferingSessionRepo_RoomDoubleBook(t *testing.T) {
	pool := liveDB(t)
	r := pg.NewOfferingSessionRepo(&liveTxRunner{pool: pool})

	tenant, _ := uuid.NewV7()
	off, _ := uuid.NewV7()
	tenantID := tenant.String()
	ctx := tracing.WithTenantID(context.Background(), tenantID)
	// room_id is a UUID column — the ratified gate keys on Room identity.
	room := uuid.Must(uuid.NewV7()).String()
	roomB := uuid.Must(uuid.NewV7()).String()

	t.Cleanup(func() {
		cctx := context.Background()
		tx, err := pool.Begin(cctx)
		if err != nil {
			return
		}
		defer func() { _ = tx.Rollback(cctx) }()
		_, _ = tx.Exec(cctx, "SET LOCAL chora.tenant_id = '"+tenantID+"'")
		_, _ = tx.Exec(cctx, `DELETE FROM offering_sessions WHERE tenant_id = $1`, tenantID)
		_ = tx.Commit(cctx)
	})

	t0 := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	mk := func(roomID string, start, end time.Time) *offeringsession.OfferingSession {
		s, err := offeringsession.NewOfferingSession(offeringsession.NewOfferingSessionInput{
			TenantID: tenantID, OfferingID: off.String(), Title: "IT", RoomID: roomID, StartsAt: start, EndsAt: end,
		})
		if err != nil {
			t.Fatalf("NewOfferingSession: %v", err)
		}
		return s
	}

	if err := r.Save(ctx, mk(room, t0, t0.Add(2*time.Hour))); err != nil {
		t.Fatalf("first save: %v", err)
	}
	// same room_id, overlapping window → the DB EXCLUDE must reject.
	err := r.Save(ctx, mk(room, t0.Add(time.Hour), t0.Add(3*time.Hour)))
	if !errors.Is(err, offeringsession.ErrRoomDoubleBooked) {
		t.Fatalf("expected ErrRoomDoubleBooked from the DB EXCLUDE, got %v", err)
	}
	// different room_id, same window → allowed.
	if err := r.Save(ctx, mk(roomB, t0.Add(time.Hour), t0.Add(3*time.Hour))); err != nil {
		t.Fatalf("different room must be allowed, got %v", err)
	}
	// abutting (starts where the first ends), same room_id → allowed (half-open).
	if err := r.Save(ctx, mk(room, t0.Add(2*time.Hour), t0.Add(3*time.Hour))); err != nil {
		t.Fatalf("abutting session must be allowed, got %v", err)
	}
}
