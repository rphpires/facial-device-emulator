package emulator

import (
	"github.com/rphpires/facial-device-emulator/internal/models"
)

// Emulator is the interface every device emulator implements
type Emulator interface {
	Start() error
	Stop() error
	IsRunning() bool
	GetInfo() models.Device
	GenerateEvent() error
	GetType() string
	GetTotalUsers() (int, error)
}
