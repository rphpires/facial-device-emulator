package handlers

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestWriteSSE pins the wire format. It is strict on purpose: the
// browser's EventSource silently drops a malformed frame, and a missing
// "\n" shows up as "real time does not work" with no visible error on
// either side.
func TestWriteSSE(t *testing.T) {
	var buf bytes.Buffer

	if err := writeSSE(&buf, "device", map[string]int{"id": 7}); err != nil {
		t.Fatalf("writeSSE returned an error: %v", err)
	}

	got := buf.String()

	if !strings.HasPrefix(got, "event: device\n") {
		t.Errorf("frame does not start with the event line:\n%q", got)
	}
	if !strings.HasSuffix(got, "\n\n") {
		t.Errorf("frame does not end with a blank line, EventSource will never dispatch it:\n%q", got)
	}

	dataLine := strings.TrimSuffix(strings.SplitN(got, "\n", 2)[1], "\n\n")
	if !strings.HasPrefix(dataLine, "data: ") {
		t.Fatalf("second line is not data:\n%q", got)
	}

	var decoded map[string]int
	if err := json.Unmarshal([]byte(strings.TrimPrefix(dataLine, "data: ")), &decoded); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	if decoded["id"] != 7 {
		t.Errorf("payload = %v, want id 7", decoded)
	}
}

// TestWriteSSE_PayloadHasNoNewline ensures the serialized JSON carries no
// literal "\n": a newline inside data: would end the frame halfway and
// deliver truncated JSON.
func TestWriteSSE_PayloadHasNoNewline(t *testing.T) {
	var buf bytes.Buffer

	if err := writeSSE(&buf, "device", map[string]string{"name": "North\nGate"}); err != nil {
		t.Fatalf("writeSSE returned an error: %v", err)
	}

	body := strings.TrimSuffix(buf.String(), "\n\n")
	if strings.Count(body, "\n") != 1 {
		t.Errorf("frame has an extra newline inside the payload:\n%q", buf.String())
	}
}
