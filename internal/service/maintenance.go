package service

import (
	"fmt"
	"time"

	"github.com/wyw14/solo-038/internal/maintenance"
	"github.com/wyw14/solo-038/internal/model"
)

const (
	pressUnitCardPrefix = "press-unit-"
	dryerFanCardEntry   = "dryer-fan-entry"
	dryerFanCardExit    = "dryer-fan-exit"
	unwindBrakeCard     = "unwind-brake"
)

var pressUnitNames = []string{"cyan", "magenta", "yellow", "black"}

var dryerFanCards = []string{dryerFanCardEntry, dryerFanCardExit}

type maintenanceCardSpec struct {
	id    string
	kind  maintenance.Kind
	name  string
	limit float64
}

func defaultMaintenanceCards() []maintenanceCardSpec {
	specs := []maintenanceCardSpec{
		{id: dryerFanCardEntry, kind: maintenance.KindDryerFan, name: "烘干风机-入口", limit: 2000},
		{id: dryerFanCardExit, kind: maintenance.KindDryerFan, name: "烘干风机-出口", limit: 2000},
		{id: unwindBrakeCard, kind: maintenance.KindUnwindBrake, name: "放卷制动件", limit: 5000},
	}
	for _, unit := range pressUnitNames {
		specs = append(specs, maintenanceCardSpec{id: pressUnitCardPrefix + unit, kind: maintenance.KindPressUnit, name: "印刷单元-" + unit, limit: 50000})
	}
	return specs
}

// restoreMaintenance rebuilds the maintenance projection from the journal and
// registers any default card that has no registration event yet.
func (runtime *Runtime) restoreMaintenance(current model.Job, now time.Time) error {
	rebuilt, err := maintenance.RebuildFromJournal(runtime.journal)
	if err != nil {
		return err
	}
	for _, spec := range defaultMaintenanceCards() {
		if _, exists := rebuilt.Card(spec.id); exists {
			continue
		}
		card, err := maintenance.NewCard(spec.id, spec.kind, spec.name, spec.limit, now)
		if err != nil {
			return err
		}
		if err := rebuilt.Register(card); err != nil {
			return err
		}
		if _, err := maintenance.PersistRegistration(runtime.journal, current, card, now); err != nil {
			return err
		}
	}
	runtime.maintenance = rebuilt
	return nil
}

// accrueMaintenance meters runtime usage onto a card, persists the delta, and
// records an incident when the component crosses its life limit.
func (runtime *Runtime) accrueMaintenance(current model.Job, id string, units float64, source string, now time.Time) error {
	newlyOverdue, err := runtime.maintenance.Accrue(id, units, source, now)
	if err != nil {
		return err
	}
	if units == 0 {
		return nil
	}
	if _, err := maintenance.PersistUsage(runtime.journal, current, id, units, source, now); err != nil {
		return err
	}
	if newlyOverdue {
		runtime.RecordIncident("warning", "maintenance", fmt.Sprintf("component %s exceeded its maintenance limit", id))
	}
	return nil
}

// tickMaintenance settles running fan hours and persists each settlement.
func (runtime *Runtime) tickMaintenance(current model.Job, now time.Time) error {
	settlements, err := runtime.maintenance.Tick(now)
	if err != nil {
		return err
	}
	for _, settlement := range settlements {
		if settlement.Units > 0 {
			if _, err := maintenance.PersistUsage(runtime.journal, current, settlement.ID, settlement.Units, "fan_runtime", now); err != nil {
				return err
			}
		}
		if settlement.NewlyOverdue {
			runtime.RecordIncident("warning", "maintenance", fmt.Sprintf("component %s exceeded its maintenance limit", settlement.ID))
		}
	}
	return nil
}

// stopMaintenanceFans settles fan hours when production stops.
func (runtime *Runtime) stopMaintenanceFans(current model.Job, now time.Time) error {
	for _, fan := range dryerFanCards {
		settled, err := runtime.maintenance.SetRunning(fan, false, now)
		if err != nil {
			return err
		}
		if settled > 0 {
			if _, err := maintenance.PersistUsage(runtime.journal, current, fan, settled, "fan_runtime", now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (runtime *Runtime) MaintenanceCards() []maintenance.Card {
	return runtime.maintenance.Cards()
}

func (runtime *Runtime) MaintenanceCard(id string) (maintenance.Card, bool) {
	return runtime.maintenance.Card(id)
}

func (runtime *Runtime) MaintenanceWindows() ([]maintenance.Window, error) {
	current, ok := runtime.jobs.Current()
	if !ok {
		return nil, fmt.Errorf("there is no active job")
	}
	now := time.Now()
	if err := runtime.tickMaintenance(current, now); err != nil {
		return nil, err
	}
	return maintenance.ForecastWindows(runtime.maintenance.Cards(), now), nil
}

func (runtime *Runtime) RecordMaintenanceService(id, note string, performedAt time.Time) (maintenance.ServiceRecord, error) {
	current, ok := runtime.jobs.Current()
	if !ok {
		return maintenance.ServiceRecord{}, fmt.Errorf("there is no active job")
	}
	now := time.Now()
	record, err := runtime.maintenance.RecordService(id, note, performedAt, now)
	if err != nil {
		return maintenance.ServiceRecord{}, err
	}
	if _, err := maintenance.PersistService(runtime.journal, current, id, record, now); err != nil {
		return maintenance.ServiceRecord{}, err
	}
	return record, nil
}

func (runtime *Runtime) ReplaceMaintenanceComponent(id string, newLimit float64, note string) (maintenance.Replacement, error) {
	current, ok := runtime.jobs.Current()
	if !ok {
		return maintenance.Replacement{}, fmt.Errorf("there is no active job")
	}
	now := time.Now()
	replacement, err := runtime.maintenance.Replace(id, newLimit, note, now)
	if err != nil {
		return maintenance.Replacement{}, err
	}
	if _, err := maintenance.PersistReplacement(runtime.journal, current, id, replacement, now); err != nil {
		return maintenance.Replacement{}, err
	}
	return replacement, nil
}

// BackfillMaintenanceUsage records historical counts that were never metered.
// The backfill can only add usage; overdue history already on the card is
// preserved, and a backfill that crosses the limit adds a new overdue marker.
func (runtime *Runtime) BackfillMaintenanceUsage(id string, units float64, effectiveAt time.Time, reason string) (maintenance.Card, error) {
	current, ok := runtime.jobs.Current()
	if !ok {
		return maintenance.Card{}, fmt.Errorf("there is no active job")
	}
	if reason == "" {
		return maintenance.Card{}, fmt.Errorf("backfill reason is required")
	}
	now := time.Now()
	newlyOverdue, err := runtime.maintenance.Backfill(id, units, effectiveAt, reason, now)
	if err != nil {
		return maintenance.Card{}, err
	}
	if _, err := maintenance.PersistBackfill(runtime.journal, current, id, units, effectiveAt, reason, now); err != nil {
		return maintenance.Card{}, err
	}
	if newlyOverdue {
		runtime.RecordIncident("warning", "maintenance", fmt.Sprintf("component %s exceeded its maintenance limit", id))
	}
	card, _ := runtime.maintenance.Card(id)
	return card, nil
}
