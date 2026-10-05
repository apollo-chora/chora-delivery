-- 001_create_extensions_and_enums.down.sql
DROP FUNCTION IF EXISTS update_updated_at CASCADE;
DROP TYPE IF EXISTS campus_day_of_week;
DROP TYPE IF EXISTS booking_status;
DROP TYPE IF EXISTS campus_check_in_method;
DROP TYPE IF EXISTS campus_attendance_status;
DROP TYPE IF EXISTS section_status;
DROP TYPE IF EXISTS room_type;
DROP TYPE IF EXISTS term_type;
