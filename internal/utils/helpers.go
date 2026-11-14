package utils

import (
	"errors"
	"fmt"
	"math/rand"
	"net"
	"strings"
	"syscall"
)

// GenerateMacAddress generates a random MAC address
func GenerateMacAddress() string {
	// The first byte of a MAC address must be even and can't be 0 or 255
	firstByte := rand.Intn(127)*2 + 2

	// Generate the next 5 bytes randomly
	macBytes := []int{firstByte}
	for i := 0; i < 5; i++ {
		macBytes = append(macBytes, rand.Intn(256))
	}

	// Format the MAC address in a readable format
	macAddress := make([]string, len(macBytes))
	for i, b := range macBytes {
		macAddress[i] = fmt.Sprintf("%02x", b)
	}

	return strings.Join(macAddress, ":")
}

// RandomAccessNotDone returns true with a 20% probability
func RandomAccessNotDone() bool {
	return rand.Intn(100) < 20
}

// ErrPortInUse marks a bind failure caused by another listener on the port.
var ErrPortInUse = errors.New("port already in use")

// ListenPort binds 0.0.0.0:<port> (every interface, which Docker needs) and
// returns the listener. Binding synchronously is what lets an emulator's
// Start report a taken port instead of claiming success. An
// "address already in use" failure is wrapped in ErrPortInUse with a
// message that says what to do about it.
func ListenPort(port int) (net.Listener, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err == nil {
		return ln, nil
	}
	if isAddrInUse(err) {
		return nil, fmt.Errorf("%w: port %d is bound by another process or device (%v)", ErrPortInUse, port, err)
	}
	return nil, fmt.Errorf("listen on port %d: %w", port, err)
}

// isAddrInUse recognizes EADDRINUSE on Unix and WSAEADDRINUSE on Windows,
// whose errno does not match syscall.EADDRINUSE.
func isAddrInUse(err error) bool {
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "only one usage of each socket address")
}
