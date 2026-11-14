package models

// Device is an emulated access control device.
type Device struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	IPAddress     string `json:"ip_address"`
	Port          int    `json:"port"`
	Model         string `json:"model"`
	Enabled       int    `json:"enabled"`
	Type          int    `json:"type"`
	Status        string `json:"status"`
	EventInterval int    `json:"event_interval"`
	TotalUsers    int    `json:"total_users"`
	LogEnabled    int    `json:"log_enabled"`
}

// DefaultEventServerPort is the port of the event receiver (the access
// control system under test) a device pushes to before the client configures
// one through httpHosts (Hikvision) or setConfig (Dahua). It is the single
// source for the repository fallbacks; the RemotePort seed in
// assets/migrations/V001_create_emulator_schema.sql uses the same value.
const DefaultEventServerPort = "8080"
