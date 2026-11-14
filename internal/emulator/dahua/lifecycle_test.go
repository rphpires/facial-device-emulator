package dahua

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgconn"
	"github.com/jackc/pgproto3/v2"
	"github.com/jackc/pgx/v4"

	"github.com/rphpires/facial-device-emulator/internal/models"
	"github.com/rphpires/facial-device-emulator/internal/trace"
)

// noRowsDB is a DBInterface where every lookup finds nothing, so the
// repository falls back to its defaults (standalone mode, no users).
type noRowsDB struct{}

type noRow struct{}

func (noRow) Scan(...interface{}) error { return pgx.ErrNoRows }

type emptyRows struct{}

func (emptyRows) Close()                                         {}
func (emptyRows) Err() error                                     { return nil }
func (emptyRows) CommandTag() pgconn.CommandTag                  { return nil }
func (emptyRows) FieldDescriptions() []pgproto3.FieldDescription { return nil }
func (emptyRows) Next() bool                                     { return false }
func (emptyRows) Scan(...interface{}) error                      { return pgx.ErrNoRows }
func (emptyRows) Values() ([]interface{}, error)                 { return nil, nil }
func (emptyRows) RawValues() [][]byte                            { return nil }

func (noRowsDB) Query(context.Context, string, ...interface{}) (pgx.Rows, error) {
	return emptyRows{}, nil
}
func (noRowsDB) QueryRow(context.Context, string, ...interface{}) pgx.Row { return noRow{} }
func (noRowsDB) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return nil, nil
}
func (noRowsDB) Begin(context.Context) (pgx.Tx, error) { return nil, errors.New("no tx") }
func (noRowsDB) Ping(context.Context) error            { return nil }

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func newLifecycleEmulator(t *testing.T, port int) *Emulator {
	t.Helper()
	dev := models.Device{ID: 1, Name: "race", Port: port, EventInterval: 1, IPAddress: "127.0.0.1"}
	return NewEmulator(noRowsDB{}, dev, trace.NewTracer(), nil)
}

// TestStartStopRace cycles Start/Stop while other goroutines read the
// running state, generate events and hold eventManager streams open. Run
// with -race: it guards the per-run stop channel and the atomic running flag.
func TestStartStopRace(t *testing.T) {
	port := freePort(t)
	e := newLifecycleEmulator(t, port)

	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				_ = e.IsRunning()
				_ = e.GetStatus()
				_ = e.remoteURL()
				e.setRemote("127.0.0.1", "1")
				_ = e.GenerateEvent()
				_ = e.runStop()
				time.Sleep(time.Millisecond)
			}
		}()
	}

	client := &http.Client{Timeout: 500 * time.Millisecond}
	url := fmt.Sprintf("http://127.0.0.1:%d/cgi-bin/eventManager.cgi?action=attach&codes=[All]", port)
	for i := 0; i < 15; i++ {
		if err := e.Start(); err != nil {
			t.Fatalf("cycle %d: Start: %v", i, err)
		}
		if resp, err := client.Get(url); err == nil {
			resp.Body.Close()
		}
		if err := e.Stop(); err != nil {
			t.Fatalf("cycle %d: Stop: %v", i, err)
		}
	}
	close(done)
	wg.Wait()

	if e.IsRunning() {
		t.Error("emulator still running after the last Stop")
	}
}

// TestStartFailsWhenPortTaken: a taken port must fail Start, not report
// success while the server goroutine dies.
func TestStartFailsWhenPortTaken(t *testing.T) {
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	e := newLifecycleEmulator(t, ln.Addr().(*net.TCPAddr).Port)
	if err := e.Start(); err == nil {
		_ = e.Stop()
		t.Fatal("Start succeeded on a taken port")
	}
	if e.IsRunning() {
		t.Error("IsRunning after a failed Start")
	}
}

// brokenListener fails Accept, as a listener whose socket died would.
type brokenListener struct{}

func (brokenListener) Accept() (net.Conn, error) { return nil, errors.New("socket closed underneath") }
func (brokenListener) Close() error              { return nil }
func (brokenListener) Addr() net.Addr            { return &net.TCPAddr{} }

// TestServeExitMarksDead: when Serve returns on its own (not via Stop) the
// run is marked dead, so the manager's watchdog sees it and restarts it.
func TestServeExitMarksDead(t *testing.T) {
	e := newLifecycleEmulator(t, freePort(t))
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	stop := e.runStop()

	// Replace the run's server loop with one whose listener is broken.
	e.lifeMu.Lock()
	srv := e.server
	e.lifeMu.Unlock()
	e.serve(&http.Server{Handler: srv.Handler}, brokenListener{}, stop)

	if e.IsRunning() {
		t.Error("IsRunning true after Serve exited unexpectedly")
	}
	select {
	case <-stop:
	default:
		t.Error("the run's stop channel was not closed")
	}
	_ = srv.Close() // release the real listener of the run
}
