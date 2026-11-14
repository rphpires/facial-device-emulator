-- PostgreSQL bootstrap, run once when the container's data volume is created.
-- The application creates the "service" and "emulator" schemas and all
-- tables on startup (assets/migrations/V001_create_emulator_schema.sql).

CREATE DATABASE emulator_db;
GRANT ALL PRIVILEGES ON DATABASE emulator_db TO emulator;
