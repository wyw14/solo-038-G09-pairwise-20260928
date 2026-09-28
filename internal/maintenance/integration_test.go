package maintenance

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/wyw14/solo-038/internal/journal"
	"github.com/wyw14/solo-038/internal/model"
)

func TestRebuildFromJournal(t *testing.T) {
	store := journal.NewStore(filepath.Join(t.TempDir(), "events.jsonl"))
	job := model.NewJob(testStart)
	card, err := NewCard("brake-1", KindUnwindBrake, "放卷制动件", 100, testStart)
	if err != nil {
		t.Fatalf("new card: %v", err)
	}
	if _, err := PersistRegistration(store, job, card, testStart); err != nil {
		t.Fatalf("persist registration: %v", err)
	}
	now := testStart.Add(24 * time.Hour)
	if _, err := PersistUsage(store, job, "brake-1", 40, "feed_web", testStart.Add(time.Hour)); err != nil {
		t.Fatalf("persist usage: %v", err)
	}
	if _, err := PersistBackfill(store, job, "brake-1", 80, testStart.Add(2*time.Hour), "历史补录", now); err != nil {
		t.Fatalf("persist backfill: %v", err)
	}
	serviceRecord := ServiceRecord{Note: "检修", Usage: 120, PerformedAt: now, RecordedAt: now}
	if _, err := PersistService(store, job, "brake-1", serviceRecord, now); err != nil {
		t.Fatalf("persist service: %v", err)
	}
	replacement := Replacement{Note: "换新", OldLimit: 100, NewLimit: 1000, FinalUsage: 120, ReplacedAt: now.Add(time.Hour)}
	if _, err := PersistReplacement(store, job, "brake-1", replacement, now.Add(time.Hour)); err != nil {
		t.Fatalf("persist replacement: %v", err)
	}

	state, err := RebuildFromJournal(store)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	rebuilt, ok := state.Card("brake-1")
	if !ok {
		t.Fatal("card missing after rebuild")
	}
	if rebuilt.Usage != 0 {
		t.Fatalf("usage after replacement must be 0, got %v", rebuilt.Usage)
	}
	if rebuilt.LifeLimit != 1000 {
		t.Fatalf("new life limit = %v, want 1000", rebuilt.LifeLimit)
	}
	if len(rebuilt.Overdues) != 1 {
		t.Fatalf("overdue history (120 crossed at 100) must survive replacement, got %d", len(rebuilt.Overdues))
	}
	overdue := rebuilt.Overdues[0]
	if overdue.Usage != 120 {
		t.Fatalf("overdue usage = %v, want 120", overdue.Usage)
	}
	if !overdue.EffectiveAt.Equal(testStart.Add(2 * time.Hour)) {
		t.Fatalf("overdue effective time = %v, want backfill effective time", overdue.EffectiveAt)
	}
	if len(rebuilt.Records) != 1 || rebuilt.Records[0].Note != "检修" {
		t.Fatalf("service records lost: %+v", rebuilt.Records)
	}
	if len(rebuilt.Replacements) != 1 || rebuilt.Replacements[0].FinalUsage != 120 {
		t.Fatalf("replacement history lost: %+v", rebuilt.Replacements)
	}
}

func TestRebuildEmptyStore(t *testing.T) {
	store := journal.NewStore(filepath.Join(t.TempDir(), "events.jsonl"))
	state, err := RebuildFromJournal(store)
	if err != nil {
		t.Fatalf("rebuild empty store: %v", err)
	}
	if len(state.Cards()) != 0 {
		t.Fatalf("expected no cards, got %d", len(state.Cards()))
	}
}

func TestRebuildRejectsUnknownComponent(t *testing.T) {
	store := journal.NewStore(filepath.Join(t.TempDir(), "events.jsonl"))
	job := model.NewJob(testStart)
	if _, err := PersistUsage(store, job, "ghost", 1, "feed_web", testStart); err != nil {
		t.Fatalf("persist: %v", err)
	}
	if _, err := RebuildFromJournal(store); err == nil {
		t.Fatal("usage for an unregistered component must fail rebuild")
	}
}
