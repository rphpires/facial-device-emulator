package assets

import (
	"embed"
	"io/fs"
)

//go:embed all:web all:migrations
var root embed.FS

// Templates returns the FS holding every template under "web/templates/*.html".
func Templates() fs.FS { return root }

// StaticFS returns the "web/static" sub-filesystem, served through http.FS.
func StaticFS() fs.FS {
	sub, err := fs.Sub(root, "web/static")
	if err != nil {
		panic(err)
	}
	return sub
}

// MigrationSQL returns the embedded V001 migration.
func MigrationSQL() ([]byte, error) {
	return root.ReadFile("migrations/V001_create_emulator_schema.sql")
}

// MigrationFiles returns the "migrations" sub-filesystem used by the
// migration runner (internal/database/migrate.go). MigrationSQL still
// serves the schema validator, which only knows the baseline.
func MigrationFiles() (fs.FS, error) {
	return fs.Sub(root, "migrations")
}

//go:embed device/photo.jpg
var devicePhoto []byte

// DevicePhoto returns a copy of the placeholder JPEG (686x686, a neutral
// silhouette drawn for this project) that emulated devices attach to their
// events. A copy, so no caller can corrupt the shared bytes.
func DevicePhoto() []byte {
	return append([]byte(nil), devicePhoto...)
}
