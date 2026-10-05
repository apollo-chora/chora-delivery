-- 001_create_extensions_and_enums.down.sql

DROP FUNCTION IF EXISTS update_updated_at() CASCADE;

DROP TYPE IF EXISTS partner_status;
DROP TYPE IF EXISTS endorsement_level;
DROP TYPE IF EXISTS capstone_status;
DROP TYPE IF EXISTS placement_status;
DROP TYPE IF EXISTS wbl_application_status;
DROP TYPE IF EXISTS internship_status;
