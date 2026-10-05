-- 0015_submission_answer_option_id_text.up.sql
--
-- Aligns chora_delivery.submission_answers.mcq_choice_id{,s} with the actual
-- option_id shape produced upstream by chora-creation (positional letters
-- "A"/"B"/"C"/"D" today; opaque strings post-Authoring rev). Migration 0010
-- declared these columns as UUID/UUID[], so every learner autosave PATCH
-- carrying mcq_choice_id="C" was rejected by PG with
-- `22P02 invalid input syntax for type uuid: "C"`; the FE swallowed the 500
-- and the submission released with score 0/N.
--
-- See docs/m13/handoff-be-mcq-grading-2026-05-17.md (root cause table).

ALTER TABLE submission_answers
    ALTER COLUMN mcq_choice_id  TYPE text USING mcq_choice_id::text,
    ALTER COLUMN mcq_choice_ids TYPE text[] USING mcq_choice_ids::text[];
