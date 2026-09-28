package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wyw14/solo-038/internal/service"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	runtime, err := service.NewRuntime(service.Config{
		DataDir:           t.TempDir(),
		FestoonCapacity:   120,
		RegisterGap:       50 * time.Millisecond,
		RegisterTolerance: 0.25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return NewRouter(runtime)
}

func TestMaintenanceEndToEnd(t *testing.T) {
	server := newTestServer(t)

	// Initially all cards are healthy.
	request := httptest.NewRequest(http.MethodGet, "/api/maintenance/cards", nil)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("cards status = %d", recorder.Code)
	}
	var initial []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &initial); err != nil {
		t.Fatal(err)
	}
	if len(initial) != 3 {
		t.Fatalf("cards = %d, want 3", len(initial))
	}

	// Backfill enough brake usage to cross the threshold.
	observed := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	body := bytes.NewBufferString(`{"delta":50001,"observed_at":"` + observed + `"}`)
	request = httptest.NewRequest(http.MethodPost, "/api/maintenance/cards/unwind_brake/usage", body)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("backfill status = %d body = %s", recorder.Code, recorder.Body.String())
	}

	// The startup operation is now rejected with 409.
	request = httptest.NewRequest(http.MethodPost, "/api/operations", bytes.NewBufferString(`{"kind":"feed_web"}`))
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "maintenance overdue") {
		t.Fatalf("feed_web with overdue brake: %d %s", recorder.Code, recorder.Body.String())
	}

	// Unknown components are 404.
	request = httptest.NewRequest(http.MethodGet, "/api/maintenance/cards/gizmo", nil)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown component status = %d, want 404", recorder.Code)
	}

	// Inspect the brake to resolve the latch.
	request = httptest.NewRequest(http.MethodPost, "/api/maintenance/cards/unwind_brake/service",
		bytes.NewBufferString(`{"kind":"inspection","note":"pads verified"}`))
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("inspection status = %d body = %s", recorder.Code, recorder.Body.String())
	}

	// The historical latch is still visible on the card, marked resolved.
	request = httptest.NewRequest(http.MethodGet, "/api/maintenance/cards/unwind_brake", nil)
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	var card map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if card["status"] != "ok" {
		t.Fatalf("card status = %v, want ok", card["status"])
	}
	latches, ok := card["latches"].([]any)
	if !ok || len(latches) != 1 {
		t.Fatalf("latches = %v, want one historical latch", card["latches"])
	}
	if !latches[0].(map[string]any)["resolved"].(bool) {
		t.Fatal("historical overdue latch must remain on record and be marked resolved")
	}
}
