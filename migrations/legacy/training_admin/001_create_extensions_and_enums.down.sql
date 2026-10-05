-- 001_create_extensions_and_enums.down.sql

DROP FUNCTION IF EXISTS update_updated_at() CASCADE;

DROP TYPE IF EXISTS day_of_week;
DROP TYPE IF EXISTS recurrence_type;
DROP TYPE IF EXISTS enrollment_status;
DROP TYPE IF EXISTS program_status;
DROP TYPE IF EXISTS request_status;
DROP TYPE IF EXISTS trainee_request_type;
DROP TYPE IF EXISTS application_status;
DROP TYPE IF EXISTS check_in_method;
DROP TYPE IF EXISTS attendance_status;
DROP TYPE IF EXISTS delivery_mode;
DROP TYPE IF EXISTS session_status;
