package config

import (
	"net/url"
	"testing"

	"github.com/jackc/pgconn"
	_ "time/tzdata" // zone lookups must not depend on the host
)

func TestConfigYamlUsesPort7070AndLocalhost(t *testing.T) {
	cfg, err := Load("../../configs/config.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 7070 {
		t.Errorf("server.port = %d, want 7070", cfg.Server.Port)
	}
	if cfg.ServiceDB.Host != "127.0.0.1" {
		t.Errorf("service_db.host = %q, want \"127.0.0.1\"", cfg.ServiceDB.Host)
	}
}

func TestDefaultTimezone(t *testing.T) {
	t.Setenv("EMULATOR_DEFAULT_TIMEZONE", "")
	cfg, err := Load("../../configs/config.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Emulator.DefaultTimezone != "UTC" {
		t.Errorf("emulator.default_timezone = %q, want \"UTC\"", cfg.Emulator.DefaultTimezone)
	}

	t.Setenv("EMULATOR_DEFAULT_TIMEZONE", "Asia/Tokyo")
	cfg, err = Load("../../configs/config.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Emulator.DefaultTimezone != "Asia/Tokyo" {
		t.Errorf("env override: got %q, want \"Asia/Tokyo\"", cfg.Emulator.DefaultTimezone)
	}

	t.Setenv("EMULATOR_DEFAULT_TIMEZONE", "Not/AZone")
	if _, err := Load("../../configs/config.yaml"); err == nil {
		t.Error("an unknown time zone must fail Load")
	}
}

func TestPostgresURLEscapesCredentials(t *testing.T) {
	c := DatabaseConfig{
		Driver: "postgres", Host: "db.local", Port: 5432,
		Username: "us@r:x", Password: "p@ss:w/rd#1?%", Database: "emu db", Schema: "a,b",
	}
	got, err := c.PostgresURL()
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("not a valid URL: %v (%s)", err, got)
	}
	if u.Host != "db.local:5432" {
		t.Errorf("host = %q", u.Host)
	}
	if u.User.Username() != "us@r:x" {
		t.Errorf("user = %q", u.User.Username())
	}
	if pw, _ := u.User.Password(); pw != "p@ss:w/rd#1?%" {
		t.Errorf("password = %q", pw)
	}
	if u.Path != "/emu db" {
		t.Errorf("path = %q", u.Path)
	}
	if u.Query().Get("search_path") != "a,b" || u.Query().Get("sslmode") != "disable" {
		t.Errorf("query = %q", u.RawQuery)
	}

	// pgx must parse it back to the same credentials.
	pc, err := pgconn.ParseConfig(got)
	if err != nil {
		t.Fatalf("pgconn.ParseConfig: %v", err)
	}
	if pc.User != "us@r:x" || pc.Password != "p@ss:w/rd#1?%" || pc.Database != "emu db" || pc.Host != "db.local" {
		t.Errorf("pgconn parsed %+v", pc)
	}
}

func TestPostgresURLRejectsIncompleteConfig(t *testing.T) {
	for name, c := range map[string]DatabaseConfig{
		"driver":   {Driver: "mysql", Host: "h", Database: "d"},
		"host":     {Driver: "postgres", Database: "d"},
		"database": {Driver: "postgres", Host: "h"},
	} {
		if _, err := c.PostgresURL(); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_FORMAT", "json")
	t.Setenv("DB_RESET_ON_MISMATCH", "true")
	t.Setenv("DB_PASSWORD", "s3cr@t")
	cfg, err := Load("../../configs/config.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Log.Level != "debug" || cfg.Log.Format != "json" {
		t.Errorf("log = %+v", cfg.Log)
	}
	if !cfg.ServiceDB.ResetOnMismatch || !cfg.EmulatorDB.ResetOnMismatch {
		t.Error("DB_RESET_ON_MISMATCH not applied")
	}
	if cfg.ServiceDB.Password != "s3cr@t" {
		t.Errorf("password = %q", cfg.ServiceDB.Password)
	}

	t.Setenv("SERVER_PORT", "abc")
	if _, err := Load("../../configs/config.yaml"); err == nil {
		t.Error("a non-numeric SERVER_PORT must fail Load")
	}
}
