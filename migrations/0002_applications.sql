-- =============================================================================
-- chora-delivery : 0002_applications.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : agent-a4c8ded2201f2125e (S6.1 — A-Course-App)
-- Date          : 2026-05-09
-- Architecture  : Architecture Review locked 2026-05-07 (Tier 1 D1) — R+ Rhythm+
--
-- Adds the Course Application aggregate (`docs/design/ux_course_application.md`):
--   - applications                — root aggregate
--   - application_state_history   — append-only audit trail
--
-- RLS scopes per tenant_id (chora.tenant_id session var) + gcid (chora.user_gcid)
-- so a learner only ever sees their own applications. Cross-DB queries forbidden
-- per .claude/rules/ddd-enforcement.md — chora-delivery owns this table.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- ENUMs — application status
-- -----------------------------------------------------------------------------
CREATE TYPE application_status AS ENUM (
    'draft',
    'submitted',
    'under_review',
    'offer_made',
    'accepted',
    'paid',
    'enrolled',
    'withdrawn',
    'rejected'
);

-- -----------------------------------------------------------------------------
-- applications — Course Application aggregate root
--
-- Idempotent natural key: UNIQUE (tenant_id, course_id, gcid) post-Submitted
-- (a learner can submit only one application per course at a time; the repo
-- promotes Drafts to Submitted, so the unique index is total).
-- -----------------------------------------------------------------------------
CREATE TABLE applications (
    application_id              UUID                PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id                   UUID                NOT NULL,
    course_id                   UUID                NOT NULL,
    class_id                    UUID                NULL,           -- chosen class instance (optional)
    gcid                        UUID                NOT NULL,       -- applicant's GCID

    status                      application_status  NOT NULL DEFAULT 'draft',

    -- Lifecycle timestamps (stamped by the domain on Transition).
    offer_expires_at            TIMESTAMPTZ         NULL,
    accepted_at                 TIMESTAMPTZ         NULL,
    paid_at                     TIMESTAMPTZ         NULL,
    enrolled_at                 TIMESTAMPTZ         NULL,
    withdrawn_at                TIMESTAMPTZ         NULL,

    -- External integrations.
    -- Per ADR-164 Stage C (2026-05-24): this column now stores the Stripe
    -- handle (Checkout Session ID pre-capture, PaymentIntent ID post-
    -- capture) sourced from chora.payments.application_payment.*
    -- Pub/Sub events. chora-delivery NO LONGER calls the Stripe SDK
    -- directly — the canonical handle is materialised here via the
    -- events/payments_subscriber.go::HandleApplicationPaymentCaptured
    -- path. Kept for audit + idempotent-replay cross-correlation.
    stripe_payment_intent_id    TEXT                NULL,           -- cs_… pre-capture / pi_… post-capture (sourced from chora.payments.* events)
    invoice_id                  TEXT                NULL,           -- CHO-INV-… (append-only post-payment)
    singpass_session_id         TEXT                NULL,           -- audit trail

    -- Decision metadata.
    rejected_reason             TEXT                NULL,
    withdrawn_reason            TEXT                NULL,

    -- Funding skeleton (full SkillsFutures integration deferred to M17).
    funding_lines               JSONB               NOT NULL DEFAULT '[]',

    created_at                  TIMESTAMPTZ         NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ         NOT NULL DEFAULT now(),
    deleted_at                  TIMESTAMPTZ         NULL,

    UNIQUE (tenant_id, course_id, gcid)
);

CREATE INDEX idx_applications_tenant       ON applications (tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_applications_gcid         ON applications (gcid) WHERE deleted_at IS NULL;
CREATE INDEX idx_applications_course       ON applications (course_id) WHERE deleted_at IS NULL;
CREATE INDEX idx_applications_status       ON applications (status) WHERE deleted_at IS NULL;
CREATE INDEX idx_applications_pi           ON applications (stripe_payment_intent_id)
    WHERE stripe_payment_intent_id IS NOT NULL;

CREATE TRIGGER trg_applications_updated_at
    BEFORE UPDATE ON applications
    FOR EACH ROW EXECUTE FUNCTION delivery_set_updated_at();

ALTER TABLE applications ENABLE ROW LEVEL SECURITY;

-- Tenant isolation policy — composes with database-level domain isolation.
CREATE POLICY tenant_isolation ON applications
    FOR ALL
    USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- Per-user isolation policy — when chora.user_gcid is set, restrict to that
-- user's own applications. Useful for HTTP routes that read on behalf of a
-- learner; admin (R+) flows do NOT set chora.user_gcid so they see all.
CREATE POLICY user_isolation ON applications
    FOR SELECT
    USING (
        current_setting('chora.user_gcid', true) IS NULL
        OR current_setting('chora.user_gcid', true) = ''
        OR gcid = current_setting('chora.user_gcid', true)::uuid
    );

-- -----------------------------------------------------------------------------
-- application_state_history — append-only audit trail
--
-- Mirrors application.HistoryEntry struct in the domain. NEVER UPDATE / DELETE.
-- Trigger enforces at the DB level.
-- -----------------------------------------------------------------------------
CREATE TABLE application_state_history (
    history_id        UUID                PRIMARY KEY DEFAULT gen_random_uuid(),
    application_id    UUID                NOT NULL REFERENCES applications(application_id) ON DELETE RESTRICT,
    tenant_id         UUID                NOT NULL,
    from_status       application_status  NOT NULL,
    to_status         application_status  NOT NULL,
    reason            TEXT                NULL,
    transitioned_at   TIMESTAMPTZ         NOT NULL DEFAULT now()
);

CREATE INDEX idx_app_history_app    ON application_state_history (application_id);
CREATE INDEX idx_app_history_tenant ON application_state_history (tenant_id);

CREATE OR REPLACE FUNCTION enforce_app_history_append_only()
RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION 'application_state_history is append-only: % rejected', TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_app_history_no_update
    BEFORE UPDATE ON application_state_history
    FOR EACH ROW EXECUTE FUNCTION enforce_app_history_append_only();

CREATE TRIGGER trg_app_history_no_delete
    BEFORE DELETE ON application_state_history
    FOR EACH ROW EXECUTE FUNCTION enforce_app_history_append_only();

ALTER TABLE application_state_history ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON application_state_history
    FOR ALL
    USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

COMMIT;
