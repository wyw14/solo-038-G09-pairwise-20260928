package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-038/internal/maintenance"
)

type maintenanceUsageRequest struct {
	Delta      float64   `json:"delta"`
	ObservedAt time.Time `json:"observed_at"`
}

type maintenanceServiceRequest struct {
	Kind     string  `json:"kind"`
	NewLimit float64 `json:"new_limit,omitempty"`
	Note     string  `json:"note,omitempty"`
}

func (handler *Handler) maintenanceCards(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, handler.runtime.MaintenanceCards())
}

func (handler *Handler) maintenanceCard(writer http.ResponseWriter, request *http.Request) {
	card, err := handler.runtime.MaintenanceCard(chi.URLParam(request, "component"))
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, maintenance.ErrUnknownComponent) {
			status = http.StatusNotFound
		}
		writeError(writer, status, err)
		return
	}
	writeJSON(writer, http.StatusOK, card)
}

func (handler *Handler) maintenanceWindows(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, handler.runtime.MaintenanceWindows())
}

func (handler *Handler) backfillMaintenanceUsage(writer http.ResponseWriter, request *http.Request) {
	var input maintenanceUsageRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	record, err := handler.runtime.BackfillMaintenanceUsage(chi.URLParam(request, "component"), input.Delta, input.ObservedAt)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, record)
}

func (handler *Handler) recordMaintenanceService(writer http.ResponseWriter, request *http.Request) {
	var input maintenanceServiceRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	record, err := handler.runtime.RecordMaintenanceService(chi.URLParam(request, "component"), input.Kind, input.NewLimit, input.Note)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, record)
}
