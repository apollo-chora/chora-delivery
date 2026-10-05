// offering_session_double_book_test.go — prepare-smoke for ratified CHO-2191 /
// ADR-237: the pg upsert must write the extracted room_id + ends_at columns so
// the DB EXCLUDE constraint (mig 0052) can see them. The live constraint
// behaviour is proven in offering_session_double_book_integration_test.go
// (tag integration).
package pg_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/apollo-chora/chora-common/tracing"
	"github.com/apollo-chora/chora-delivery/internal/adapter/repo/pg"
	offeringsession "github.com/apollo-chora/chora-delivery/internal/domain/offering_session"
)

func mkPgSession(roomID string) *offeringsession.OfferingSession {
	t0 := time.Now().UTC().Truncate(time.Second)
	s, _ := offeringsession.NewOfferingSession(offeringsession.NewOfferingSessionInput{
		TenantID: tenantID, OfferingID: courseID, Title: "S", RoomID: roomID,
		StartsAt: t0, EndsAt: t0.Add(time.Hour),
	})
	return s
}

func TestOfferingSessionRepo_Save_WritesRoomAndEndsAtColumns(t *testing.T) {
	t.Parallel()
	q := &stubQuerier{}
	r := pg.NewOfferingSessionRepo(&stubTxRunner{q: q})
	ctx := tracing.WithTenantID(context.Background(), tenantID)

	if err := r.Save(ctx, mkPgSession("01985e7f-6666-7abc-8def-0000000000aa")); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if !strings.Contains(q.sqls[0], "SET LOCAL chora.tenant_id") {
		t.Fatalf("first SQL must apply RLS; got %q", q.sqls[0])
	}
	last := q.sqls[len(q.sqls)-1]
	if !strings.Contains(last, "room_id") {
		t.Fatalf("upsert must write the room_id column (ratified gate key); got %q", last)
	}
	if !strings.Contains(last, "ends_at") {
		t.Fatalf("upsert must write the ends_at column; got %q", last)
	}
}
