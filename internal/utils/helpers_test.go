package utils

import (
	"errors"
	"net"
	"testing"
)

// TestListenPortReportsPortInUse: a second bind on the same port must fail
// with ErrPortInUse, which is what Start reports instead of a false success.
func TestListenPortReportsPortInUse(t *testing.T) {
	first, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	port := first.Addr().(*net.TCPAddr).Port

	ln, err := ListenPort(port)
	if err == nil {
		ln.Close()
		t.Fatalf("second bind on port %d succeeded, want an error", port)
	}
	if !errors.Is(err, ErrPortInUse) {
		t.Errorf("err = %v, want ErrPortInUse", err)
	}
}

func TestListenPortBindsFreePort(t *testing.T) {
	ln, err := ListenPort(0)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}
