-- 001_create_extensions_and_enums.down.sql
DROP FUNCTION IF EXISTS update_updated_at() CASCADE;
DROP TYPE IF EXISTS exam_appeal_status;
DROP TYPE IF EXISTS exam_incident_severity;
DROP TYPE IF EXISTS exam_incident_type;
DROP TYPE IF EXISTS exam_proctor_session_status;
DROP TYPE IF EXISTS exam_registration_status;
DROP TYPE IF EXISTS exam_sitting_status;
DROP TYPE IF EXISTS exam_contract_status;
