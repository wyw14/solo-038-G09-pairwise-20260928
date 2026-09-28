package maintenance

import (
	"path/filepath"
	"testing"
	"time"
)

func testTracker(t *testing.T) *Tracker {
	t.Helper()
	return NewTracker(nil, clock())
}

// clock returns a fixed base time for deterministic tests.
func clock() func() time.Time {
	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	return func() time.Time { return base }
}

func TestSpecs(t *testing.T) {
	spec, err := SpecFor(ComponentPressUnit)
	if err != nil {
		t.Fatalf("spec for press_unit: %v", err)
	}
	if spec.Unit != "impressions" {
		t.Fatalf("press unit = %q, want impressions", spec.Unit)
	}
	if _, err := ParseComponent("gearbox"); err == nil {
		t.Fatal("expected unknown component error")
	}
}

func TestUsageTriggersDueSoonThenOverdue(t *testing.T) {
	tracker := testTracker(t)
	now := clock()()
	if _, err := tracker.RecordUsage(ComponentPressUnit, 90000, now.Add(-2*24*time.Hour), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.RecordUsage(ComponentPressUnit, 1000, now.Add(-1*24*time.Hour), "test"); err != nil {
		t.Fatal(err)
	}
	card, err := tracker.Card(ComponentPressUnit)
	if err != nil {
		t.Fatal(err)
	}
	if card.Status != StatusDueSoon {
		t.Fatalf("status = %q, want due_soon", card.Status)
	}
	if _, err := tracker.RecordUsage(ComponentPressUnit, 10000, now, "test"); err != nil {
		t.Fatal(err)
	}
	card, _ = tracker.Card(ComponentPressUnit)
	if card.Status != StatusOverdue {
		t.Fatalf("status = %q, want overdue", card.Status)
	}
	if card.UnresolvedOverdue != 1 {
		t.Fatalf("unresolved overdue = %d, want 1", card.UnresolvedOverdue)
	}
	if len(tracker.Blocked()) != 1 {
		t.Fatalf("blocked = %v, want press_unit", tracker.Blocked())
	}
}

func TestInspectionExtendsThresholdAndResolvesLatch(t *testing.T) {
	tracker := testTracker(t)
	now := clock()()
	if _, err := tracker.RecordUsage(ComponentDryerFan, 12000, now.Add(-time.Hour), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.RecordUsage(ComponentDryerFan, 100, now, "test"); err != nil {
		t.Fatal(err)
	}
	if len(tracker.Blocked()) == 0 {
		t.Fatal("expected dryer_fan to be blocked")
	}
	if _, err := tracker.RecordService(ComponentDryerFan, ServiceInspection, 0, "fan checked, bearing fine"); err != nil {
		t.Fatal(err)
	}
	card, _ := tracker.Card(ComponentDryerFan)
	if card.Status != StatusOK {
		t.Fatalf("status after inspection = %q, want ok; remaining %.0f/%.0f", card.Status, card.Remaining, card.Threshold)
	}
	if card.Threshold != 24000 {
		t.Fatalf("threshold = %.0f, want 24000 (one lifespan extended)", card.Threshold)
	}
	if card.UnresolvedOverdue != 0 {
		t.Fatalf("unresolved overdue = %d, want 0", card.UnresolvedOverdue)
	}
	// The historical overdue latch remains on record but is marked resolved.
	if len(card.Latches) != 1 || !card.Latches[0].Resolved {
		t.Fatalf("latches = %+v, want one resolved historical latch", card.Latches)
	}
}

func TestReplacementResetsUsageAndAppliesNewLimit(t *testing.T) {
	current := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	tracker := NewTracker(nil, func() time.Time { return current })
	for index := 0; index < 11; index++ {
		if _, err := tracker.RecordUsage(ComponentUnwindBrake, 5000, current.Add(-time.Duration(11-index)*time.Hour), "test"); err != nil {
			t.Fatal(err)
		}
	}
	if len(tracker.Blocked()) == 0 {
		t.Fatal("expected unwind_brake to be blocked before replacement")
	}
	current = current.Add(time.Hour)
	if _, err := tracker.RecordService(ComponentUnwindBrake, ServiceReplacement, 75000, "heavy duty brake pads"); err != nil {
		t.Fatal(err)
	}
	card, _ := tracker.Card(ComponentUnwindBrake)
	if card.Segment != 2 {
		t.Fatalf("segment = %d, want 2", card.Segment)
	}
	if card.Usage != 0 {
		t.Fatalf("usage = %.0f, want 0 after replacement", card.Usage)
	}
	if card.Limit != 75000 {
		t.Fatalf("limit = %.0f, want 75000", card.Limit)
	}
	if card.Status != StatusOK {
		t.Fatalf("status = %q, want ok", card.Status)
	}
	current = current.Add(time.Hour)
	if _, err := tracker.RecordUsage(ComponentUnwindBrake, 70000, current, "test"); err != nil {
		t.Fatal(err)
	}
	card, _ = tracker.Card(ComponentUnwindBrake)
	if card.Status != StatusDueSoon {
		t.Fatalf("status = %q, want due_soon under new lifespan", card.Status)
	}
}

// TestBackfillNeverErasesOverdue is the audit guarantee: a part that
// accrued real usage past its threshold, then got inspected, must still
// show the original overdue when the late reading is entered after the
// fact. Backfill can only add latches, never remove them.
func TestBackfillNeverErasesOverdue(t *testing.T) {
	tracker := testTracker(t)
	now := clock()()
	// Current readings show the part safely under limit.
	if _, err := tracker.RecordUsage(ComponentPressUnit, 50000, now.Add(-time.Hour), "test"); err != nil {
		t.Fatal(err)
	}
	// Inspection performed today.
	if _, err := tracker.RecordService(ComponentPressUnit, ServiceInspection, 0, "routine check"); err != nil {
		t.Fatal(err)
	}
	// Historical usage from last week is backfilled: in the original lifespan
	// the part had crossed 100k. This reopens the overdue history.
	if _, err := tracker.Backfill(ComponentPressUnit, 60000, now.Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	card, _ := tracker.Card(ComponentPressUnit)
	var foundHistorical bool
	for _, latch := range card.Latches {
		if latch.Segment == 1 && latch.Resolved {
			foundHistorical = true
			if latch.ResolvedBy != "inspection" {
				t.Fatalf("latch resolved by %q, want inspection", latch.ResolvedBy)
			}
		}
	}
	if !foundHistorical {
		t.Fatalf("expected a resolved historical latch for segment 1; got %+v", card.Latches)
	}
	if card.UnresolvedOverdue != 0 {
		t.Fatalf("unresolved overdue after backfill + inspection = %d, want 0", card.UnresolvedOverdue)
	}
}

// TestBackfillRevealsHiddenOverdue covers the case where the backfilled
// reading proves the part crossed the threshold and was never serviced.
func TestBackfillRevealsHiddenOverdue(t *testing.T) {
	tracker := testTracker(t)
	now := clock()()
	if _, err := tracker.RecordUsage(ComponentDryerFan, 100, now.Add(-time.Hour), "test"); err != nil {
		t.Fatal(err)
	}
	// Late historical reading shows it actually exceeded the limit.
	if _, err := tracker.Backfill(ComponentDryerFan, 12050, now.Add(-30*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	card, _ := tracker.Card(ComponentDryerFan)
	if card.Status != StatusOverdue {
		t.Fatalf("status = %q, want overdue after backfill", card.Status)
	}
	if card.UnresolvedOverdue != 1 {
		t.Fatalf("unresolved overdue = %d, want 1", card.UnresolvedOverdue)
	}
	if len(tracker.Blocked()) != 1 {
		t.Fatal("expected dryer_fan to block after backfill")
	}
}

func TestJournalReplay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "maintenance.jsonl")
	tracker, err := OpenTracker(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := tracker.RecordUsage(ComponentPressUnit, 100001, now.Add(-time.Hour), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.RecordService(ComponentPressUnit, ServiceInspection, 0, "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.RecordUsage(ComponentUnwindBrake, 1000, now.Add(-30*time.Minute), "test"); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenTracker(path)
	if err != nil {
		t.Fatal(err)
	}
	card, err := reopened.Card(ComponentPressUnit)
	if err != nil {
		t.Fatal(err)
	}
	if card.Segment != 1 || card.Threshold != 200000 || card.Usage != 100001 {
		t.Fatalf("replayed press card = %+v", card)
	}
	if len(card.Latches) != 1 || !card.Latches[0].Resolved {
		t.Fatalf("replayed latches = %+v, want one resolved", card.Latches)
	}
	brake, _ := reopened.Card(ComponentUnwindBrake)
	if brake.Usage != 1000 {
		t.Fatalf("replayed brake usage = %.0f, want 1000", brake.Usage)
	}
}

func TestWindowPrediction(t *testing.T) {
	tracker := testTracker(t)
	now := clock()()
	// 10000 units per day over two days -> 100k threshold due in ~8 days.
	if _, err := tracker.RecordUsage(ComponentPressUnit, 10000, now.Add(-48*time.Hour), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.RecordUsage(ComponentPressUnit, 10000, now, "test"); err != nil {
		t.Fatal(err)
	}
	windows := tracker.Windows()
	if len(windows) != 1 {
		t.Fatalf("windows = %d, want 1", len(windows))
	}
	window := windows[0]
	if window.Component != ComponentPressUnit {
		t.Fatalf("window component = %q", window.Component)
	}
	if window.EstimatedDueAt == nil {
		t.Fatal("expected estimated due time")
	}
	days := window.EstimatedDueAt.Sub(now).Hours() / 24
	if days < 7 || days > 9 {
		t.Fatalf("estimated due in %.1f days, want ~8", days)
	}
}

func TestSingleReadingHasNoWindow(t *testing.T) {
	tracker := testTracker(t)
	now := clock()()
	if _, err := tracker.RecordUsage(ComponentUnwindBrake, 100, now, "test"); err != nil {
		t.Fatal(err)
	}
	if windows := tracker.Windows(); len(windows) != 0 {
		t.Fatalf("windows = %d, want 0 with one reading", len(windows))
	}
}
