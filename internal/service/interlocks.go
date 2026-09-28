package service

import (
	"sort"
	"strings"
	"time"
)

type Interlock struct {
	Name      string    `json:"name"`
	Satisfied bool      `json:"satisfied"`
	Detail    string    `json:"detail"`
	CheckedAt time.Time `json:"checked_at"`
}

func (runtime *Runtime) Interlocks() []Interlock {
	now := time.Now().UTC()
	_, _, _, threaded, _ := runtime.web.Snapshot()
	status, _ := runtime.dryerControl.Status()
	ready, _, _, fault, _ := runtime.folder.Snapshot()
	maintenanceDetail := "all components are within their service life"
	overdue := runtime.maintenance.Overdue()
	if len(overdue) > 0 {
		ids := make([]string, 0, len(overdue))
		for _, card := range overdue {
			ids = append(ids, card.ID)
		}
		maintenanceDetail = "overdue components: " + strings.Join(ids, ", ")
	}
	values := []Interlock{
		{Name: "web_threaded", Satisfied: threaded, Detail: "paper web is fenced and threaded", CheckedAt: now},
		{Name: "dryer_ready", Satisfied: status == "ready", Detail: "dryer damper and profile startup are complete", CheckedAt: now},
		{Name: "folder_ready", Satisfied: ready, Detail: fault, CheckedAt: now},
		{Name: "maintenance_clear", Satisfied: len(overdue) == 0, Detail: maintenanceDetail, CheckedAt: now},
		{Name: "press_stopped_for_splice", Satisfied: runtime.pressControl.CanSplice(), Detail: "current stop epoch has zero-speed proof", CheckedAt: now},
	}
	sort.Slice(values, func(left, right int) bool { return values[left].Name < values[right].Name })
	return values
}
