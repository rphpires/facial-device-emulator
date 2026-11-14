package dahua

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rphpires/facial-device-emulator/internal/trace"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgconn"
	"github.com/jackc/pgx/v4"
)

// fakeDB records the Exec calls it receives; the rest of DBInterface is not
// exercised by the face write path.
type fakeDB struct {
	execArgs [][]interface{}
}

func (f *fakeDB) Query(ctx context.Context, q string, args ...interface{}) (pgx.Rows, error) {
	return nil, nil
}

func (f *fakeDB) QueryRow(ctx context.Context, q string, args ...interface{}) pgx.Row { return nil }

func (f *fakeDB) Exec(ctx context.Context, q string, args ...interface{}) (pgconn.CommandTag, error) {
	f.execArgs = append(f.execArgs, args)
	return pgconn.CommandTag("INSERT 0 1"), nil
}

func (f *fakeDB) Begin(ctx context.Context) (pgx.Tx, error) { return nil, nil }

func (f *fakeDB) Ping(ctx context.Context) error { return nil }

func newFaceTestEmulator(db *fakeDB, deviceID int) *Emulator {
	return &Emulator{
		tracer:   trace.NewTracer(),
		repo:     NewRepository(db, deviceID),
		stopChan: make(chan struct{}),
	}
}

// Some clients send UserID as a JSON string — "UserID": "1" — and without a
// Content-Type. A binder that only accepts numbers drops the face with a 400
// and the user shows up in the UI as having no face enrolled.
func TestFaceInfoManagerPost_AcceptsUserIDNumberAndString(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name string
		body string
	}{
		{"numeric UserID", `{"UserID":1,"Info":{"PhotoData":["ZmFrZQ=="]}}`},
		{"UserID string", `{"UserID":"1","Info":{"PhotoData":["ZmFrZQ=="]}}`},
		{"PhotoData as a bare string", `{"UserID":"1","Info":{"PhotoData":"ZmFrZQ=="}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := &fakeDB{}
			e := newFaceTestEmulator(db, 111)

			router := gin.New()
			router.POST("/cgi-bin/FaceInfoManager.cgi", e.handleFaceInfoManagerPost)

			req := httptest.NewRequest(http.MethodPost,
				"/cgi-bin/FaceInfoManager.cgi?action=add", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
			}
			if len(db.execArgs) != 1 {
				t.Fatalf("writes = %d, want 1", len(db.execArgs))
			}

			args := db.execArgs[0]
			if len(args) != 3 {
				t.Fatalf("INSERT args = %d, want 3: %v", len(args), args)
			}
			if got, want := args[0], 111; got != want {
				t.Errorf("device_id = %v, want %v", got, want)
			}
			if got, want := args[1], 1; got != want {
				t.Errorf("user_id = %v, want %v", got, want)
			}
			if md5, ok := args[2].(string); !ok || md5 == "" {
				t.Errorf("md5_hash = %v, want non-empty hash", args[2])
			}
		})
	}
}
