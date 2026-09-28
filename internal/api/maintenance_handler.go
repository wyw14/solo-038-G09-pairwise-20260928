package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

type serviceRequest struct {
	Note        string    `json:"note"`
	PerformedAt time.Time `json:"performed_at"`
}

type replaceRequest struct {
	NewLimit float64 `json:"new_limit"`
	Note     string  `json:"note"`
}

type backfillRequest struct {
	Units       float64   `json:"units"`
	EffectiveAt time.Time `json:"effective_at"`
	Reason      string    `json:"reason"`
}

func (handler *Handler) maintenanceCards(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, handler.runtime.MaintenanceCards())
}

func (handler *Handler) maintenanceWindows(writer http.ResponseWriter, request *http.Request) {
	windows, err := handler.runtime.MaintenanceWindows()
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusOK, windows)
}

func (handler *Handler) maintenanceCard(writer http.ResponseWriter, request *http.Request) {
	card, ok := handler.runtime.MaintenanceCard(chi.URLParam(request, "cardID"))
	if !ok {
		writeError(writer, http.StatusNotFound, fmt.Errorf("maintenance component %q is not registered", chi.URLParam(request, "cardID")))
		return
	}
	writeJSON(writer, http.StatusOK, card)
}

func (handler *Handler) recordMaintenanceService(writer http.ResponseWriter, request *http.Request) {
	cardID := chi.URLParam(request, "cardID")
	if _, ok := handler.runtime.MaintenanceCard(cardID); !ok {
		writeError(writer, http.StatusNotFound, fmt.Errorf("maintenance component %q is not registered", cardID))
		return
	}
	var input serviceRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	record, err := handler.runtime.RecordMaintenanceService(cardID, input.Note, input.PerformedAt)
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusCreated, record)
}

func (handler *Handler) replaceMaintenanceComponent(writer http.ResponseWriter, request *http.Request) {
	cardID := chi.URLParam(request, "cardID")
	if _, ok := handler.runtime.MaintenanceCard(cardID); !ok {
		writeError(writer, http.StatusNotFound, fmt.Errorf("maintenance component %q is not registered", cardID))
		return
	}
	var input replaceRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	replacement, err := handler.runtime.ReplaceMaintenanceComponent(cardID, input.NewLimit, input.Note)
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusCreated, replacement)
}

func (handler *Handler) backfillMaintenanceUsage(writer http.ResponseWriter, request *http.Request) {
	cardID := chi.URLParam(request, "cardID")
	if _, ok := handler.runtime.MaintenanceCard(cardID); !ok {
		writeError(writer, http.StatusNotFound, fmt.Errorf("maintenance component %q is not registered", cardID))
		return
	}
	var input backfillRequest
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	card, err := handler.runtime.BackfillMaintenanceUsage(cardID, input.Units, input.EffectiveAt, input.Reason)
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusCreated, card)
}
