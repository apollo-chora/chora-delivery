// dispatcher_extra_test.go — additional Dispatcher coverage: the error
// branches of DrainOnce / publishOne / Run (canned Store errors via a stub
// store embedding InMemoryStore — same shape as the recordingBus / stubDB
// fixtures), the mid-drain + in-poll context-cancellation paths, and the
// reconstructEnvelope fallback branches (nil envelope, empty fields,
// schema_version variants, unparseable timestamps).
package outbox_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apollo-chora/chora-delivery/internal/adapter/outbox"
)

// stubStore embeds a real InMemoryStore and injects canned errors on the
// Store-port methods — the same canned-error pattern the dispatcher tests
// already use for the Bus (recordingBus) and the store tests for the DB
// (stubDB).
type stubStore struct {
	*outbox.InMemoryStore

	fetchErr    error
	fetchCancel func() // invoked inside FetchPending, e.g. to cancel ctx
	markPubErr  error
	markFailErr error
	deadlErr    error
}

func (s *stubStore) FetchPending(ctx context.Context, limit int) ([]outbox.Row, error) {
	if s.fetchCancel != nil {
		s.fetchCancel()
	}
	if s.fetchErr != nil {
		return nil, s.fetchErr
	}
	return s.InMemoryStore.FetchPending(ctx, limit)
}

func (s *stubStore) MarkPublished(ctx context.Context, id string) error {
	if s.markPubErr != nil {
		return s.markPubErr
	}
	return s.InMemoryStore.MarkPublished(ctx, id)
}

func (s *stubStore) MarkFailed(ctx context.Context, id string, errMsg string) error {
	if s.markFailErr != nil {
		return s.markFailErr
	}
	return s.InMemoryStore.MarkFailed(ctx, id, errMsg)
}

func (s *stubStore) Deadletter(ctx context.Context, id, failureReason string, attemptCount int) error {
	if s.deadlErr != nil {
		return s.deadlErr
	}
	return s.InMemoryStore.Deadletter(ctx, id, failureReason, attemptCount)
}

func insertRawRow(t *testing.T, store *outbox.InMemoryStore, row outbox.Row) {
	t.Helper()
	if err := store.Insert(context.Background(), row); err != nil {
		t.Fatalf("Insert: %v", err)
	}
}

func TestDispatcher_DrainOnce_SurfacesFetchError(t *testing.T) {
	t.Parallel()
	store := &stubStore{InMemoryStore: outbox.NewInMemoryStore(), fetchErr: errors.New("fetch boom")}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: &recordingBus{}, WorkerID: "w1", MaxAttempts: 3,
	})
	_, err := d.DrainOnce(context.Background(), 10)
	if err == nil || !errors.Is(err, context.Canceled) && err.Error() != "outbox.Dispatcher.DrainOnce: fetch boom" {
		t.Errorf("DrainOnce err = %v; want fetch-boom wrapped error", err)
	}
}

func TestDispatcher_DrainOnce_CancelledMidDrain_ReturnsPartialCount(t *testing.T) {
	t.Parallel()
	store := &stubStore{InMemoryStore: outbox.NewInMemoryStore()}
	for i := 0; i < 3; i++ {
		insertRow(t, store.InMemoryStore, "mid-"+string(rune('a'+i)), "t")
	}
	ctx, cancel := context.WithCancel(context.Background())
	store.fetchCancel = cancel

	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: &recordingBus{}, WorkerID: "w1", MaxAttempts: 3,
	})
	n, err := d.DrainOnce(ctx, 10)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("DrainOnce err = %v; want context.Canceled", err)
	}
	if n != 0 {
		t.Errorf("publish count = %d; want 0 (cancelled before any publish)", n)
	}
}

func TestDispatcher_PublishOne_SurfacesMarkPublishedError(t *testing.T) {
	t.Parallel()
	store := &stubStore{InMemoryStore: outbox.NewInMemoryStore(), markPubErr: errors.New("mark pub boom")}
	insertRow(t, store.InMemoryStore, "rMP", "t1")
	bus := &recordingBus{}

	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 3,
	})
	n, err := d.DrainOnce(context.Background(), 10)
	if err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if n != 0 {
		t.Errorf("publish count = %d; want 0 (MarkPublished failed)", n)
	}
	if bus.callCount() != 1 {
		t.Errorf("bus calls = %d; want 1 (payload was published before MarkPublished)", bus.callCount())
	}
	if len(store.Published()) != 0 {
		t.Errorf("Published = %v; want none (MarkPublished failed)", store.Published())
	}
}

func TestDispatcher_PublishOne_SurfacesMarkFailedError(t *testing.T) {
	t.Parallel()
	store := &stubStore{InMemoryStore: outbox.NewInMemoryStore(), markFailErr: errors.New("mark fail boom")}
	insertRow(t, store.InMemoryStore, "rMF", "t1")
	bus := &recordingBus{fails: 1, failErr: errors.New("pubsub blip")}

	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 3,
	})
	n, err := d.DrainOnce(context.Background(), 10)
	if err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	if n != 0 {
		t.Errorf("publish count = %d; want 0", n)
	}
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("pending = %d; want 1", len(rows))
	}
	if rows[0].RetryCount != 0 {
		t.Errorf("retry_count = %d; want 0 (MarkFailed itself failed)", rows[0].RetryCount)
	}
}

func TestDispatcher_PublishOne_SurfacesDeadletterError(t *testing.T) {
	t.Parallel()
	store := &stubStore{InMemoryStore: outbox.NewInMemoryStore(), deadlErr: errors.New("deadletter boom")}
	insertRow(t, store.InMemoryStore, "rDL", "t1")
	bus := &recordingBus{fails: 10, failErr: errors.New("permanent")}

	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 2,
	})
	for i := 0; i < 2; i++ {
		if _, err := d.DrainOnce(context.Background(), 10); err != nil {
			t.Fatalf("DrainOnce pass %d: %v", i, err)
		}
	}
	// Deadletter failed: the row must still be pending (not marked deadlettered).
	rows, _ := store.FetchPending(context.Background(), 10)
	if len(rows) != 1 {
		t.Fatalf("pending = %d; want 1 (Deadletter failed)", len(rows))
	}
	if len(store.DeadLetters()) != 0 {
		t.Errorf("DeadLetters = %d; want 0", len(store.DeadLetters()))
	}
}

func TestDispatcher_Run_LogsDrainErrorAndContinues(t *testing.T) {
	t.Parallel()
	store := &stubStore{InMemoryStore: outbox.NewInMemoryStore(), fetchErr: errors.New("fetch boom")}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: &recordingBus{}, WorkerID: "w1", MaxAttempts: 3,
		PollInterval: 5 * time.Millisecond,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	err := d.Run(ctx, 10)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("Run err = %v; want context deadline/cancel", err)
	}
}

func TestDispatcher_Run_ReturnsPreCancelledContextError(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: &recordingBus{}, WorkerID: "w1", MaxAttempts: 3,
		PollInterval: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := d.Run(ctx, 10)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("Run err = %v; want context.Canceled", err)
	}
}

// TestDispatcher_Run_ReturnsContextError_WhenDrainCancelledMidBatch — rows
// are fetched, the store cancels the ctx inside FetchPending, and the
// mid-loop ctx.Done() fires → DrainOnce returns context.Canceled → Run must
// recognize it via errors.Is and return it (its context-error exit branch).
func TestDispatcher_Run_ReturnsContextError_WhenDrainCancelledMidBatch(t *testing.T) {
	t.Parallel()
	store := &stubStore{InMemoryStore: outbox.NewInMemoryStore()}
	insertRow(t, store.InMemoryStore, "rC1", "t1")
	insertRow(t, store.InMemoryStore, "rC2", "t2")
	ctx, cancel := context.WithCancel(context.Background())
	store.fetchCancel = cancel

	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: &recordingBus{}, WorkerID: "w1", MaxAttempts: 3,
		PollInterval: time.Hour,
	})
	err := d.Run(ctx, 10)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("Run err = %v; want context.Canceled", err)
	}
}

func TestDispatcher_Run_StopsWhenCanceledDuringPoll(t *testing.T) {
	t.Parallel()
	store := &stubStore{InMemoryStore: outbox.NewInMemoryStore()}
	ctx, cancel := context.WithCancel(context.Background())
	store.fetchCancel = cancel

	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: &recordingBus{}, WorkerID: "w1", MaxAttempts: 3,
		PollInterval: time.Hour, // never fires — ctx.Done() must win
	})
	err := d.Run(ctx, 10)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("Run err = %v; want context.Canceled", err)
	}
}

// reconstructEnvelope — nil row.Envelope: defaults from the Row columns.
func TestDispatcher_ReconstructEnvelope_NilEnvelopeFallsBack(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	occurred := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	insertRawRow(t, store, outbox.Row{
		ID:             "rNil",
		TenantID:       "00000000-0000-0000-0000-0000000000aa",
		GCID:           "00000000-0000-0000-0000-0000000000bb",
		EventType:      "delivery.booking.confirmed",
		Topic:          "chora.delivery.booking.confirmed.v1",
		Payload:        []byte(`{}`),
		IdempotencyKey: "idem-rNil",
		OccurredAt:     occurred,
	})
	bus := &recordingBus{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 3,
	})
	if _, err := d.DrainOnce(context.Background(), 10); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	env := bus.calls[0].Envelope
	if env.EventID != "rNil" {
		t.Errorf("EventID = %q; want rNil", env.EventID)
	}
	if env.IdempotencyKey != "idem-rNil" {
		t.Errorf("IdempotencyKey = %q; want idem-rNil", env.IdempotencyKey)
	}
	if !env.OccurredAt.Equal(occurred) {
		t.Errorf("OccurredAt = %v; want %v (row.OccurredAt fallback)", env.OccurredAt, occurred)
	}
	if env.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d; want 1", env.SchemaVersion)
	}
	if env.SourceProject != "chora-489812" {
		t.Errorf("SourceProject = %q; want chora-489812", env.SourceProject)
	}
	if env.SourceService != "chora-delivery" {
		t.Errorf("SourceService = %q; want chora-delivery", env.SourceService)
	}
}

// reconstructEnvelope — empty string values in a non-nil envelope fall back
// to the Row columns + config defaults, and empty timestamps fall back too.
func TestDispatcher_ReconstructEnvelope_EmptyFieldsFallBack(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	occurred := time.Date(2026, 5, 12, 12, 30, 0, 0, time.UTC)
	insertRawRow(t, store, outbox.Row{
		ID:             "rEmpty",
		TenantID:       "00000000-0000-0000-0000-0000000000aa",
		GCID:           "00000000-0000-0000-0000-0000000000bb",
		EventType:      "delivery.booking.confirmed",
		Topic:          "chora.delivery.booking.confirmed.v1",
		Payload:        []byte(`{}`),
		Envelope:       map[string]string{"schema_version": ""}, // every value empty
		IdempotencyKey: "idem-rEmpty",
		OccurredAt:     occurred,
	})
	bus := &recordingBus{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 3,
	})
	if _, err := d.DrainOnce(context.Background(), 10); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	env := bus.calls[0].Envelope
	if env.EventID != "rEmpty" {
		t.Errorf("EventID = %q; want rEmpty", env.EventID)
	}
	if env.TenantID != "00000000-0000-0000-0000-0000000000aa" {
		t.Errorf("TenantID = %q; want row tenant", env.TenantID)
	}
	if env.GCID != "00000000-0000-0000-0000-0000000000bb" {
		t.Errorf("GCID = %q; want row gcid", env.GCID)
	}
	if !env.OccurredAt.Equal(occurred) {
		t.Errorf("OccurredAt = %v; want %v (empty string fallback)", env.OccurredAt, occurred)
	}
	if env.PublishedAt.IsZero() {
		t.Errorf("PublishedAt zero; want now() fallback")
	}
	if env.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d; want 1", env.SchemaVersion)
	}
}

// reconstructEnvelope — schema_version parsing variants: valid int, zero,
// non-numeric, missing (else branch).
func TestDispatcher_ReconstructEnvelope_SchemaVersionVariants(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	rows := []outbox.Row{
		{ID: "sv-zero", Topic: "chora.delivery.booking.confirmed.v1", Payload: []byte(`{}`), IdempotencyKey: "k-zero", OccurredAt: time.Now().UTC(), Envelope: map[string]string{"schema_version": "0"}},
		{ID: "sv-text", Topic: "chora.delivery.booking.confirmed.v1", Payload: []byte(`{}`), IdempotencyKey: "k-text", OccurredAt: time.Now().UTC(), Envelope: map[string]string{"schema_version": "not-a-number"}},
		{ID: "sv-valid", Topic: "chora.delivery.booking.confirmed.v1", Payload: []byte(`{}`), IdempotencyKey: "k-valid", OccurredAt: time.Now().UTC(), Envelope: map[string]string{"schema_version": "7"}},
		{ID: "sv-missing", Topic: "chora.delivery.booking.confirmed.v1", Payload: []byte(`{}`), IdempotencyKey: "k-missing", OccurredAt: time.Now().UTC(), Envelope: map[string]string{}},
	}
	for _, r := range rows {
		insertRawRow(t, store, r)
	}
	bus := &recordingBus{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 3,
	})
	if _, err := d.DrainOnce(context.Background(), 10); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	got := map[string]int32{}
	for _, c := range bus.calls {
		got[c.Envelope.EventID] = c.Envelope.SchemaVersion
	}
	if got["sv-zero"] != 1 {
		t.Errorf("sv-zero SchemaVersion = %d; want 1 (clamped)", got["sv-zero"])
	}
	if got["sv-text"] != 1 {
		t.Errorf("sv-text SchemaVersion = %d; want 1 (unparseable → 1)", got["sv-text"])
	}
	if got["sv-valid"] != 7 {
		t.Errorf("sv-valid SchemaVersion = %d; want 7", got["sv-valid"])
	}
	if got["sv-missing"] != 1 {
		t.Errorf("sv-missing SchemaVersion = %d; want 1 (else branch)", got["sv-missing"])
	}
}

// reconstructEnvelope — unparseable timestamp strings fall back (RFC3339Nano
// + RFC3339 both rejected → the function's final fallback).
func TestDispatcher_ReconstructEnvelope_BadTimeStringsFallBack(t *testing.T) {
	t.Parallel()
	store := outbox.NewInMemoryStore()
	occurred := time.Date(2026, 5, 12, 12, 45, 0, 0, time.UTC)
	insertRawRow(t, store, outbox.Row{
		ID:             "rBadTime",
		TenantID:       "t",
		EventType:      "delivery.booking.confirmed",
		Topic:          "chora.delivery.booking.confirmed.v1",
		Payload:        []byte(`{}`),
		Envelope:       map[string]string{"occurred_at": "not-a-time", "published_at": "yesterday-ish"},
		IdempotencyKey: "idem-rBadTime",
		OccurredAt:     occurred,
	})
	bus := &recordingBus{}
	d := outbox.NewDispatcher(outbox.DispatcherConfig{
		Store: store, Bus: bus, WorkerID: "w1", MaxAttempts: 3,
	})
	if _, err := d.DrainOnce(context.Background(), 10); err != nil {
		t.Fatalf("DrainOnce: %v", err)
	}
	env := bus.calls[0].Envelope
	if !env.OccurredAt.Equal(occurred) {
		t.Errorf("OccurredAt = %v; want %v (fallback to row.OccurredAt)", env.OccurredAt, occurred)
	}
	if env.PublishedAt.IsZero() {
		t.Errorf("PublishedAt zero; want now() fallback")
	}
}
