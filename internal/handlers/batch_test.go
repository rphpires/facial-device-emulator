package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rphpires/facial-device-emulator/internal/emulator"
	"github.com/rphpires/facial-device-emulator/internal/models"
)

// fakeFleet is an in-memory batchBackend. Devices listed in failStart fail
// to start, mimicking a port already bound by another process.
type fakeFleet struct {
	mu        sync.Mutex
	devices   map[int]models.Device
	failStart map[int]bool
	inFlight  int32
	maxFlight int32
}

func newFakeFleet(devs ...models.Device) *fakeFleet {
	f := &fakeFleet{devices: map[int]models.Device{}, failStart: map[int]bool{}}
	for _, d := range devs {
		f.devices[d.ID] = d
	}
	return f
}

func (f *fakeFleet) GetDevice(id int) (models.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devices[id]
	if !ok {
		return d, fmt.Errorf("device not found: %w", emulator.ErrDeviceNotFound)
	}
	return d, nil
}

func (f *fakeFleet) setStatus(id int, status string) {
	f.mu.Lock()
	d := f.devices[id]
	d.Status = status
	f.devices[id] = d
	f.mu.Unlock()
}

func (f *fakeFleet) Start(id int) error {
	n := atomic.AddInt32(&f.inFlight, 1)
	defer atomic.AddInt32(&f.inFlight, -1)
	for {
		m := atomic.LoadInt32(&f.maxFlight)
		if n <= m || atomic.CompareAndSwapInt32(&f.maxFlight, m, n) {
			break
		}
	}
	time.Sleep(time.Millisecond)

	if f.failStart[id] {
		return errors.New("listen tcp :4000: bind: address already in use")
	}
	f.setStatus(id, "running")
	return nil
}

func (f *fakeFleet) Stop(id int) error {
	f.setStatus(id, "stopped")
	return nil
}

func (f *fakeFleet) DeleteDevice(_ context.Context, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.devices[id]; !ok {
		return fmt.Errorf("%w: %d", emulator.ErrDeviceNotFound, id)
	}
	delete(f.devices, id)
	return nil
}

func (f *fakeFleet) UpdateDevice(_ context.Context, id int, spec emulator.DeviceSpec) (models.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.devices[id]
	if d.Status == "running" {
		return d, emulator.ErrDeviceRunning
	}
	if spec.Name != d.Name || spec.Port != d.Port || spec.Model != d.Model {
		return d, errors.New("update changed fields other than enabled")
	}
	d.Enabled = 0
	if *spec.Enabled {
		d.Enabled = 1
	}
	f.devices[id] = d
	return d, nil
}

func (f *fakeFleet) UpdateDeviceSettings(id int, logEnabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.devices[id]
	d.LogEnabled = 0
	if logEnabled {
		d.LogEnabled = 1
	}
	f.devices[id] = d
	return nil
}

func device(id int, status string, enabled int) models.Device {
	return models.Device{
		ID: id, Name: fmt.Sprintf("lab-%d", 4000+id), Model: "Hikvision",
		Port: 4000 + id, Status: status, Enabled: enabled, EventInterval: 10,
	}
}

func postBatch(t *testing.T, fleet *fakeFleet, body string) (int, batchResponse, string) {
	t.Helper()
	h := &Handler{tracer: testTracer(t), batch: fleet}
	c, rec := newJSONRequest(t, http.MethodPost, "/api/emulators/batch", body, nil)
	h.apiBatchEmulators(c)

	var resp batchResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("body is not a batch response: %v - %s", err, rec.Body.String())
		}
	}
	return rec.Code, resp, rec.Body.String()
}

func TestBatchRejectsInvalidRequests(t *testing.T) {
	tooMany := make([]string, maxBatchSize+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprint(i + 1)
	}

	cases := []struct {
		name string
		body string
		want string
	}{
		{"malformed JSON", `{"ids":[1,`, "Invalid request body"},
		{"unknown action", `{"ids":[1],"action":"reboot"}`, "unknown action"},
		{"missing action", `{"ids":[1]}`, "unknown action"},
		{"empty ids", `{"ids":[],"action":"start"}`, "at least one"},
		{"non-positive id", `{"ids":[1,0],"action":"stop"}`, "invalid device ID"},
		{"ids of the wrong type", `{"ids":["a"],"action":"stop"}`, "Invalid request body"},
		{"too many ids", `{"ids":[` + strings.Join(tooMany, ",") + `],"action":"start"}`, "batch too large"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fleet := newFakeFleet(device(1, "stopped", 1))
			code, _, body := postBatch(t, fleet, tc.body)
			if code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 - %s", code, body)
			}
			if !strings.Contains(body, tc.want) {
				t.Errorf("body = %s, want it to mention %q", body, tc.want)
			}
		})
	}
}

// Duplicates in ids are applied once; the cap counts distinct devices.
func TestBatchDeduplicatesIDs(t *testing.T) {
	fleet := newFakeFleet(device(1, "stopped", 1))
	code, resp, body := postBatch(t, fleet, `{"ids":[1,1,1],"action":"start"}`)
	if code != http.StatusOK {
		t.Fatalf("status %d - %s", code, body)
	}
	if resp.Requested != 1 || len(resp.Results) != 1 {
		t.Errorf("requested=%d results=%d, want 1 and 1", resp.Requested, len(resp.Results))
	}
}

// A partially failed batch is still a 200 with per-item outcomes, in the
// order the IDs were sent.
func TestBatchStartMixedResults(t *testing.T) {
	fleet := newFakeFleet(
		device(1, "stopped", 1),
		device(2, "running", 1), // already running: skipped
		device(3, "stopped", 0), // disabled: fails
		device(4, "stopped", 1), // bind error: fails
		device(5, "stopped", 1),
	)
	fleet.failStart[4] = true

	code, resp, body := postBatch(t, fleet, `{"ids":[1,2,3,4,5,99],"action":"start"}`)
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200 - %s", code, body)
	}

	if resp.Action != "start" || resp.Requested != 6 {
		t.Errorf("action=%q requested=%d", resp.Action, resp.Requested)
	}
	if resp.Succeeded != 3 || resp.Skipped != 1 || resp.Failed != 3 {
		t.Errorf("succeeded=%d skipped=%d failed=%d, want 3/1/3", resp.Succeeded, resp.Skipped, resp.Failed)
	}

	wantOK := map[int]bool{1: true, 2: true, 3: false, 4: false, 5: true, 99: false}
	for i, r := range resp.Results {
		if want := []int{1, 2, 3, 4, 5, 99}[i]; r.ID != want {
			t.Errorf("results[%d].id = %d, want %d (input order)", i, r.ID, want)
		}
		if r.OK != wantOK[r.ID] {
			t.Errorf("device %d: ok=%v, want %v (%+v)", r.ID, r.OK, wantOK[r.ID], r)
		}
		if !r.OK && r.Error == "" {
			t.Errorf("device %d failed without an error message", r.ID)
		}
	}
	if resp.Results[1].Skipped == "" {
		t.Errorf("device 2 should be reported as skipped: %+v", resp.Results[1])
	}
	if !strings.Contains(resp.Results[5].Error, "not found") {
		t.Errorf("device 99: error = %q, want not found", resp.Results[5].Error)
	}
}

func TestBatchStopDeleteEnableDisable(t *testing.T) {
	fleet := newFakeFleet(device(1, "running", 1), device(2, "stopped", 1), device(3, "stopped", 0))

	_, resp, _ := postBatch(t, fleet, `{"ids":[1,2],"action":"stop"}`)
	if resp.Succeeded != 2 || resp.Skipped != 1 {
		t.Errorf("stop: %+v", resp)
	}

	// Disabling a running device stops it first, and the update keeps
	// every other field.
	fleet.setStatus(1, "running")
	_, resp, _ = postBatch(t, fleet, `{"ids":[1,2,3],"action":"disable"}`)
	if resp.Failed != 0 || resp.Skipped != 1 {
		t.Errorf("disable: %+v", resp)
	}
	for _, id := range []int{1, 2, 3} {
		if d, _ := fleet.GetDevice(id); d.Enabled != 0 || d.Status != "stopped" {
			t.Errorf("device %d after disable: %+v", id, d)
		}
	}

	_, resp, _ = postBatch(t, fleet, `{"ids":[1,3],"action":"enable"}`)
	if resp.Succeeded != 2 || resp.Failed != 0 {
		t.Errorf("enable: %+v", resp)
	}

	_, resp, _ = postBatch(t, fleet, `{"ids":[2],"action":"enable_log"}`)
	if d, _ := fleet.GetDevice(2); resp.Failed != 0 || d.LogEnabled != 1 {
		t.Errorf("enable_log: %+v, device %+v", resp, d)
	}

	_, resp, _ = postBatch(t, fleet, `{"ids":[1,2,42],"action":"delete"}`)
	if resp.Succeeded != 2 || resp.Failed != 1 {
		t.Errorf("delete: %+v", resp)
	}
	if _, err := fleet.GetDevice(1); err == nil {
		t.Error("device 1 still exists after delete")
	}
}

// runBatch must actually overlap work but never exceed the limit.
func TestRunBatchBoundsConcurrency(t *testing.T) {
	var devs []models.Device
	ids := make([]int, 0, 200)
	for i := 1; i <= 200; i++ {
		devs = append(devs, device(i, "stopped", 1))
		ids = append(ids, i)
	}
	fleet := newFakeFleet(devs...)

	results := runBatch(context.Background(), ids, 8, func(_ context.Context, id int) error {
		return fleet.Start(id)
	})

	if len(results) != 200 {
		t.Fatalf("got %d results, want 200", len(results))
	}
	if max := atomic.LoadInt32(&fleet.maxFlight); max > 8 || max < 2 {
		t.Errorf("max in flight = %d, want between 2 and 8", max)
	}
}

// Items not reached before the deadline fail instead of being silently
// dropped.
func TestRunBatchReportsItemsAfterTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := runBatch(ctx, []int{1, 2, 3}, 1, func(context.Context, int) error { return nil })
	for _, r := range results {
		if r.OK || r.Error == "" {
			t.Errorf("result after cancellation = %+v, want a failure", r)
		}
	}
}
