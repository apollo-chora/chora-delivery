-- =============================================================================
-- chora-delivery : 0009_test_sets.up.sql
--
-- Domain        : Content Delivery (5 core)
-- Database      : chora_delivery
-- Author        : agent — chora-delivery test-sets Lane A (B-FE-X5)
-- Date          : 2026-05-16
-- Architecture  : Architecture Review locked 2026-05-07 + ADR-155 D5
--                 (append-only-on-publish; aggregate invariant #4 per
--                 .claude/rules/ddd-enforcement.md)
--
-- Purpose:
--   TestSet aggregate persistence — per chora-contracts/openapi/
--   delivery-test-sets.yaml. Lane A scope: 6 endpoints covering
--   create / get / add-question / update-question / remove-question /
--   publish. ARCHIVED state is Tier 2 (out of Lane A).
--
-- Aggregates owned:
--   - test_sets             — TestSet aggregate root
--   - test_set_questions    — per-question inclusion rows (composition)
--
-- Cross-domain reference (no FK per ddd-enforcement #3):
--   - test_set_questions.question_atom_id → chora_creation.questions.question_id
--     (cross-DB queries FORBIDDEN — validated via gRPC at the HTTP layer)
--
-- Resilience-priority directive (feedback_resilience_priority):
--   - PRIMARY KEY on test_set_id is UUIDv7 (callers supply pre-minted IDs)
--   - Soft delete via deleted_at (per ddd-enforcement #5)
--   - RLS policies on both tables — tenant_id scope (test_sets) +
--     parent-test-set tenant scope (test_set_questions; the child carries
--     no tenant_id, derives via the FK).
--   - Partial unique index on (test_set_id) where state='PUBLISHED' is
--     unnecessary — PUBLISHED is a state transition on a single row, not
--     a duplicate row.
-- =============================================================================

BEGIN;

-- -----------------------------------------------------------------------------
-- test_sets — TestSet aggregate root
-- -----------------------------------------------------------------------------
CREATE TABLE test_sets (
    test_set_id     UUID         PRIMARY KEY,                     -- UUIDv7
    tenant_id       UUID         NOT NULL,
    author_gcid     UUID         NOT NULL,
    title           VARCHAR(256) NOT NULL,
    description     TEXT,
    state           VARCHAR(32)  NOT NULL DEFAULT 'DRAFT'
        CHECK (state IN ('DRAFT', 'PUBLISHED', 'ARCHIVED')),
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    published_at    TIMESTAMPTZ,
    deleted_at      TIMESTAMPTZ
);

CREATE INDEX idx_test_sets_tenant_author
    ON test_sets (tenant_id, author_gcid)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_test_sets_tenant_state
    ON test_sets (tenant_id, state)
    WHERE deleted_at IS NULL;

CREATE TRIGGER trg_test_sets_updated_at
    BEFORE UPDATE ON test_sets
    FOR EACH ROW EXECUTE FUNCTION delivery_set_updated_at();

ALTER TABLE test_sets ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON test_sets
    FOR ALL USING (tenant_id = current_setting('chora.tenant_id', true)::uuid);

-- -----------------------------------------------------------------------------
-- test_set_questions — per-question inclusion rows
-- -----------------------------------------------------------------------------
CREATE TABLE test_set_questions (
    test_set_question_id UUID         PRIMARY KEY,                -- UUIDv7
    test_set_id          UUID         NOT NULL
        REFERENCES test_sets(test_set_id) ON DELETE CASCADE,
    question_atom_id     UUID         NOT NULL,                   -- cross-DB ref
    question_type        VARCHAR(64)  NOT NULL
        CHECK (question_type IN ('mcq', 'oe')),
    display_order        INTEGER      NOT NULL DEFAULT 0
        CHECK (display_order >= 0),
    points               NUMERIC(10,2) NOT NULL DEFAULT 1.0
        CHECK (points > 0),
    created_at           TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ  NOT NULL DEFAULT now(),
    deleted_at           TIMESTAMPTZ
);

CREATE INDEX idx_test_set_questions_test_set_order
    ON test_set_questions (test_set_id, display_order)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_test_set_questions_atom
    ON test_set_questions (question_atom_id)
    WHERE deleted_at IS NULL;

CREATE TRIGGER trg_test_set_questions_updated_at
    BEFORE UPDATE ON test_set_questions
    FOR EACH ROW EXECUTE FUNCTION delivery_set_updated_at();

-- The child carries no tenant_id; RLS derives the scope via the parent FK.
-- This composes with the test_sets policy: a row visible to the caller's
-- tenant is the only path through which any test_set_questions row is
-- visible.
ALTER TABLE test_set_questions ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON test_set_questions
    FOR ALL USING (
        test_set_id IN (
            SELECT test_set_id FROM test_sets
            WHERE tenant_id = current_setting('chora.tenant_id', true)::uuid
        )
    );

-- -----------------------------------------------------------------------------
-- Grants — app_rw + app_ro receive privileges via the default-privileges
-- mechanism in 9999_grant_app_roles.sql. New tables created here inherit
-- those grants automatically.
-- -----------------------------------------------------------------------------

COMMIT;

-- =============================================================================
-- VERIFICATION (run manually after apply):
--
--   SET ROLE chora_delivery_app_rw;
--   BEGIN;
--     SET LOCAL chora.tenant_id = '11111111-1111-7111-8111-111111111111';
--     INSERT INTO test_sets (test_set_id, tenant_id, author_gcid, title)
--     VALUES (
--         gen_random_uuid(),
--         '11111111-1111-7111-8111-111111111111',
--         '00000000-0000-7000-8000-000000001999',
--         'Phyllis Math Test Set 1'
--     );
--     SELECT count(*) FROM test_sets; -- 1
--   ROLLBACK;
--
--   -- Cross-tenant isolation check:
--   BEGIN;
--     SET LOCAL chora.tenant_id = '22222222-2222-7222-8222-222222222222';
--     SELECT count(*) FROM test_sets;  -- 0 (other tenant's row invisible)
--   ROLLBACK;
--   RESET ROLE;
-- =============================================================================
