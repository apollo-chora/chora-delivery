-- 001_create_extensions_and_enums.down.sql
DROP FUNCTION IF EXISTS update_updated_at() CASCADE;
DROP TYPE IF EXISTS board_status;
DROP TYPE IF EXISTS entry_type;
DROP TYPE IF EXISTS poll_status;
DROP TYPE IF EXISTS poll_type;
DROP TYPE IF EXISTS quiz_status;
