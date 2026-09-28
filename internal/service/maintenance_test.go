package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wyw14/solo-038/internal/maintenance"
)

func newTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	runtime, err := NewRuntime(Config{
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
	return runtime
}

func TestOverdueBlocksStartupOperations(t *testing.T) {
	runtime := newTestRuntime(t)
	now := time.Now().UTC()

	// Push the unwind brake past its lifespan via a backfilled reading.
	if _, err := runtime.BackfillMaintenanceUsage("unwind_brake", 50001, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	blocked := runtime.maintenance.Blocked()
	if len(blocked) != 1 || blocked[0] != maintenance.ComponentUnwindBrake {
		t.Fatalf("blocked = %v, want unwind_brake", blocked)
	}

	for _, kind := range []string{"feed_web", "start_dryer"} {
		err := runtime.ExecuteOperation(context.Background(), kind)
		if err == nil || !strings.Contains(err.Error(), "maintenance overdue") {
			t.Fatalf("%s with overdue part: err = %v, want maintenance overdue error", kind, err)
		}
	}

	// The same guard must reject the operation through CreateOperation too.
	if _, err := runtime.CreateOperation(context.Background(), "feed_web"); err == nil || !strings.Contains(err.Error(), "maintenance overdue") {
		t.Fatalf("CreateOperation feed_web: err = %v, want maintenance overdue", err)
	}

	// Non-startup operations are not blocked by maintenance.
	if err := runtime.ExecuteOperation(context.Background(), "load_roll"); err != nil && strings.Contains(err.Error(), "maintenance overdue") {
		t.Fatalf("load_roll should not be blocked by maintenance: %v", err)
	}

	// Interlock surface reports the breach.
	for _, interlock := range runtime.Interlocks() {
		if interlock.Name == "maintenance_clear" && interlock.Satisfied {
			t.Fatal("maintenance_clear interlock must be unsatisfied while overdue")
		}
	}
}

func TestInspectionClearsBlockAndBackfillPreservesHistory(t *testing.T) {
	runtime := newTestRuntime(t)
	now := time.Now().UTC()

	if _, err := runtime.BackfillMaintenanceUsage("dryer_fan", 12001, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ExecuteOperation(context.Background(), "start_dryer"); err == nil || !strings.Contains(err.Error(), "maintenance overdue") {
		t.Fatalf("start_dryer before inspection: err = %v", err)
	}

	// Inspection certifies one more lifespan; startup is permitted again.
	if _, err := runtime.RecordMaintenanceService("dryer_fan", "inspection", 0, "fan verified"); err != nil {
		t.Fatal(err)
	}
	if blocked := runtime.maintenance.Blocked(); len(blocked) != 0 {
		t.Fatalf("blocked after inspection = %v, want none", blocked)
	}

	card, err := runtime.MaintenanceCard("dryer_fan")
	if err != nil {
		t.Fatal(err)
	}
	if card.Threshold != 24000 {
		t.Fatalf("threshold = %.0f, want 24000 after inspection", card.Threshold)
	}
	if len(card.Latches) != 1 || !card.Latches[0].Resolved {
		t.Fatalf("latches = %+v, want one resolved historical latch", card.Latches)
	}
}

func TestReplacementResetsCard(t *testing.T) {
	runtime := newTestRuntime(t)
	now := time.Now().UTC()
	if _, err := runtime.BackfillMaintenanceUsage("press_unit", 100001, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RecordMaintenanceService("press_unit", "replacement", 120000, "new print unit installed"); err != nil {
		t.Fatal(err)
	}
	card, err := runtime.MaintenanceCard("press_unit")
	if err != nil {
		t.Fatal(err)
	}
	if card.Segment != 2 || card.Usage != 0 || card.Limit != 120000 {
		t.Fatalf("card after replacement = %+v", card)
	}
	if card.UnresolvedOverdue != 0 {
		t.Fatalf("unresolved overdue = %d, want 0", card.UnresolvedOverdue)
	}
	// The prior segment's overdue remains visible in history.
	if len(card.Latches) != 1 || !card.Latches[0].Resolved {
		t.Fatalf("latches = %+v, want the replaced segment's latch retained", card.Latches)
	}
}

func TestBackfillIncidentRecorded(t *testing.T) {
	runtime := newTestRuntime(t)
	before := len(runtime.Incidents())
	if _, err := runtime.BackfillMaintenanceUsage("unwind_brake", 60000, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	after := runtime.Incidents()
	if len(after) <= before {
		t.Fatal("expected an incident when backfill reveals an overdue period")
	}
	if after[0].Component != "unwind_brake" {
		t.Fatalf("incident component = %q, want unwind_brake", after[0].Component)
	}
}
