-- =============================================================================
-- chora-delivery : 0016_cj2_published_public_backfill.down.sql
--
-- Reverses 0016_cj2_published_public_backfill.up.sql by flipping
-- PUBLISHED CJ#2 courses back to public=false.
--
-- WARNING: Running this down-migration breaks E2E-BE-CJ2-CATALOG-PROJECTION
-- (released CJ#2 courses disappear from /api/catalog). Only roll back if
-- the SaveCJ2 fix is being reverted concurrently in the same change set.
-- =============================================================================

BEGIN;

UPDATE courses
   SET public = FALSE
 WHERE state  = 'PUBLISHED'
   AND deleted_at IS NULL
   AND public = TRUE;

COMMIT;
