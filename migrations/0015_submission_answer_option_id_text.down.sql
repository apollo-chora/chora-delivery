-- 0015_submission_answer_option_id_text.down.sql
--
-- WARNING — reverting requires every stored mcq_choice_id / mcq_choice_ids
-- value to be a syntactically valid UUID. Rows written with positional
-- letters ("A"/"B"/"C"/"D") will fail the cast. Treat this as a manual
-- recovery path, not a routine rollback.

ALTER TABLE chora_delivery.submission_answers
    ALTER COLUMN mcq_choice_id  TYPE uuid   USING mcq_choice_id::uuid,
    ALTER COLUMN mcq_choice_ids TYPE uuid[] USING mcq_choice_ids::uuid[];
