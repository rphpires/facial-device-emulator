package config

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the application configuration.
type Config struct {
	AppVersion string         `yaml:"app_version"`
	Server     ServerConfig   `yaml:"server"`
	ServiceDB  DatabaseConfig `yaml:"service_db"`  // Main service database
	EmulatorDB DatabaseConfig `yaml:"emulator_db"` // Same database, kept conceptually separate
	Emulator   EmulatorConfig `yaml:"emulator"`
	Log        LogConfig      `yaml:"log"`
}

// LogConfig configures the service log (internal/trace).
type LogConfig struct {
	// Level is debug, info, warn or error. Default info.
	Level string `yaml:"level"`
	// Format is text or json. Default text.
	Format string `yaml:"format"`
	// File, when set, also writes the log to this path (relative to the
	// working directory), rotated by size. Empty means stdout only.
	File string `yaml:"file"`
	// MaxSizeMB is the rotation size of File. Default 10.
	MaxSizeMB int `yaml:"max_size_mb"`
	// MaxFiles is how many rotated copies of File are kept. Default 5.
	MaxFiles int `yaml:"max_files"`
}

// EmulatorConfig holds settings shared by every emulated device.
type EmulatorConfig struct {
	// DefaultTimezone is the IANA time zone (e.g. "UTC", "Europe/Berlin")
	// devices report times in until the client sets one through the device
	// protocol. Empty means UTC.
	DefaultTimezone string `yaml:"default_timezone"`
}

// DefaultTimezone is used when emulator.default_timezone is not set.
const DefaultTimezone = "UTC"

// ServerConfig holds the HTTP server settings.
type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// DatabaseConfig holds the database connection settings.
type DatabaseConfig struct {
	Driver   string `yaml:"driver"`
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Database string `yaml:"database"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Schema   string `yaml:"schema,omitempty"`
	// ResetOnMismatch lets the startup check drop and recreate the service
	// and emulator schemas when their structure is incomplete. Off by
	// default: a mismatch stops the service instead of deleting data.
	ResetOnMismatch bool `yaml:"reset_on_mismatch"`
}

// loadEnvFile loads variables from the first .env file found.
func loadEnvFile() error {
	envFiles := []string{".env.local", ".env", ".env.development"}

	for _, envFile := range envFiles {
		if _, err := os.Stat(envFile); err == nil {
			return loadSingleEnvFile(envFile)
		}
	}

	// A missing .env file is not an error.
	return nil
}

// loadSingleEnvFile loads a single .env file.
func loadSingleEnvFile(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // A missing file is not an error
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Skip blank lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse KEY=VALUE lines
		if parts := strings.SplitN(line, "=", 2); len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])

			// Strip surrounding quotes
			value = strings.Trim(value, `"'`)

			// Real environment variables take precedence
			if os.Getenv(key) == "" {
				if err := os.Setenv(key, value); err != nil {
					return fmt.Errorf("%s: setting %s: %w", filename, key, err)
				}
			}
		}
	}

	return scanner.Err()
}

// Load reads the configuration from a YAML file.
func Load(path string) (*Config, error) {
	// Load .env variables first
	if err := loadEnvFile(); err != nil {
		return nil, fmt.Errorf("loading .env file: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	config := &Config{}
	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	// EmulatorDB points at the same PostgreSQL database
	config.EmulatorDB = config.ServiceDB

	// Apply environment overrides (including those loaded from .env)
	if err := applyEnvOverrides(config); err != nil {
		return nil, fmt.Errorf("applying environment overrides: %w", err)
	}

	if config.Emulator.DefaultTimezone == "" {
		config.Emulator.DefaultTimezone = DefaultTimezone
	}
	if _, err := time.LoadLocation(config.Emulator.DefaultTimezone); err != nil {
		return nil, fmt.Errorf("emulator.default_timezone: %w", err)
	}

	if _, err := config.ServiceDB.PostgresURL(); err != nil {
		return nil, fmt.Errorf("service_db: %w", err)
	}

	return config, nil
}

// applyEnvOverrides applies overrides from environment variables.
func applyEnvOverrides(config *Config) error {
	if host := os.Getenv("SERVER_HOST"); host != "" {
		config.Server.Host = host
	}
	if err := envInt("SERVER_PORT", &config.Server.Port); err != nil {
		return err
	}

	if tz := os.Getenv("EMULATOR_DEFAULT_TIMEZONE"); tz != "" {
		config.Emulator.DefaultTimezone = tz
	}

	if v := os.Getenv("LOG_LEVEL"); v != "" {
		config.Log.Level = v
	}
	if v := os.Getenv("LOG_FORMAT"); v != "" {
		config.Log.Format = v
	}
	if v := os.Getenv("LOG_FILE"); v != "" {
		config.Log.File = v
	}

	// PostgreSQL overrides, applied in this order so DB_* wins.
	for _, prefix := range []string{"PG", "POSTGRES", "DB"} {
		if err := applyDBEnvOverrides(&config.ServiceDB, prefix); err != nil {
			return err
		}
	}

	// Keep EmulatorDB in sync
	config.EmulatorDB = config.ServiceDB

	return nil
}

// envInt parses an integer environment variable into dst when it is set.
func envInt(name string, dst *int) error {
	v := os.Getenv(name)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("%s=%q: not an integer", name, v)
	}
	*dst = n
	return nil
}

// PostgresURL returns the postgres:// connection URL. User name, password,
// database and schema are escaped, so any character is safe in them.
func (c *DatabaseConfig) PostgresURL() (string, error) {
	if c.Driver != "postgres" {
		return "", fmt.Errorf("database driver %q is not supported (want \"postgres\")", c.Driver)
	}
	if c.Host == "" {
		return "", errors.New("database host is not set")
	}
	if c.Database == "" {
		return "", errors.New("database name is not set")
	}

	host := c.Host
	if c.Port != 0 {
		host = net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	}

	q := url.Values{}
	q.Set("sslmode", "disable")
	if c.Schema != "" {
		q.Set("search_path", c.Schema)
	}

	u := url.URL{
		Scheme:   "postgres",
		Host:     host,
		Path:     "/" + c.Database,
		RawQuery: q.Encode(),
	}
	if c.Username != "" {
		u.User = url.UserPassword(c.Username, c.Password)
	}
	return u.String(), nil
}

// applyDBEnvOverrides applies <prefix>_* environment overrides to a database config.
func applyDBEnvOverrides(db *DatabaseConfig, prefix string) error {
	if host := os.Getenv(prefix + "_HOST"); host != "" {
		db.Host = host
	}
	if err := envInt(prefix+"_PORT", &db.Port); err != nil {
		return err
	}
	if database := os.Getenv(prefix + "_DATABASE"); database != "" {
		db.Database = database
	}
	if database := os.Getenv(prefix + "_DB"); database != "" {
		db.Database = database
	}
	if username := os.Getenv(prefix + "_USER"); username != "" {
		db.Username = username
	}
	if username := os.Getenv(prefix + "_USERNAME"); username != "" {
		db.Username = username
	}
	if password := os.Getenv(prefix + "_PASSWORD"); password != "" {
		db.Password = password
	}
	if driver := os.Getenv(prefix + "_DRIVER"); driver != "" {
		db.Driver = driver
	}
	if schema := os.Getenv(prefix + "_SCHEMA"); schema != "" {
		db.Schema = schema
	}
	if v := os.Getenv(prefix + "_RESET_ON_MISMATCH"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("%s_RESET_ON_MISMATCH=%q: not a boolean", prefix, v)
		}
		db.ResetOnMismatch = b
	}
	return nil
}
