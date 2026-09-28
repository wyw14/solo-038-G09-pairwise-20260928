package service

import (
	"fmt"
	"strings"
	"time"

	"github.com/wyw14/solo-038/internal/maintenance"
)

// startupOperationKinds are the new-job startup operations that must not
// run while any component has an unresolved overdue latch.
var startupOperationKinds = map[string]bool{
	"feed_web":    true,
	"start_dryer": true,
}

// errMaintenanceOverdue reports that startup is blocked by overdue parts.
func errMaintenanceOverdue(blocked []maintenance.ComponentKind) error {
	names := make([]string, 0, len(blocked))
	for _, component := range blocked {
		names = append(names, string(component))
	}
	return fmt.Errorf("maintenance overdue for %s: new jobs cannot start until service is recorded", strings.Join(names, ", "))
}

// recordUsage meters one operation's worth of component usage. Metering
// never fails the operation itself; a newly latched overdue component is
// surfaced as an incident.
func (runtime *Runtime) recordUsage(component maintenance.ComponentKind, delta float64, source string) {
	wasBlocked := runtime.maintenance.IsBlocked(component)
	if _, err := runtime.maintenance.RecordUsage(component, delta, time.Now().UTC(), source); err != nil {
		runtime.RecordIncident("error", string(component), fmt.Sprintf("usage metering failed: %v", err))
		return
	}
	if !wasBlocked && runtime.maintenance.IsBlocked(component) {
		runtime.RecordIncident("warning", string(component), "component crossed its maintenance threshold")
	}
}

// MaintenanceCards renders every component's maintenance card.
func (runtime *Runtime) MaintenanceCards() []maintenance.Card {
	return runtime.maintenance.Cards()
}

// MaintenanceCard renders one component's maintenance card.
func (runtime *Runtime) MaintenanceCard(component string) (maintenance.Card, error) {
	kind, err := maintenance.ParseComponent(component)
	if err != nil {
		return maintenance.Card{}, err
	}
	return runtime.maintenance.Card(kind)
}

// MaintenanceWindows lists predicted maintenance windows, soonest first.
func (runtime *Runtime) MaintenanceWindows() []maintenance.Window {
	return runtime.maintenance.Windows()
}

// BackfillMaintenanceUsage records historical usage against the segment
// that was active at observedAt. Backfills can only add overdue latches,
// never erase one.
func (runtime *Runtime) BackfillMaintenanceUsage(component string, delta float64, observedAt time.Time) (maintenance.UsageRecord, error) {
	kind, err := maintenance.ParseComponent(component)
	if err != nil {
		return maintenance.UsageRecord{}, err
	}
	record, err := runtime.maintenance.Backfill(kind, delta, observedAt)
	if err != nil {
		return maintenance.UsageRecord{}, err
	}
	if runtime.maintenance.IsBlocked(kind) {
		runtime.RecordIncident("warning", string(kind), "backfilled usage revealed an unresolved overdue period")
	}
	return record, nil
}

// RecordMaintenanceService journals an inspection or replacement for a
// component. Inspections extend the current lifespan threshold;
// replacements reset usage and start a fresh lifespan.
func (runtime *Runtime) RecordMaintenanceService(component, kind string, newLimit float64, note string) (maintenance.ServiceRecord, error) {
	parsed, err := maintenance.ParseComponent(component)
	if err != nil {
		return maintenance.ServiceRecord{}, err
	}
	record, err := runtime.maintenance.RecordService(parsed, maintenance.ServiceKind(kind), newLimit, note)
	if err != nil {
		return maintenance.ServiceRecord{}, err
	}
	runtime.RecordIncident("info", string(parsed), fmt.Sprintf("maintenance %s recorded", record.Kind))
	return record, nil
}
