package httpapi

import (
	"net/http"

	"example.com/paleo-prep-station/internal/contracts"
)

func (api *API) logProgress(w http.ResponseWriter, r *http.Request) {
	var request contracts.ProgressRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	value, err := api.service.LogProgress(r.Context(), r.PathValue("id"), request)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, value)
}

func (api *API) logProgressBatch(w http.ResponseWriter, r *http.Request) {
	var request contracts.ProgressBatchRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	values, err := api.service.LogProgressBatch(r.Context(), request)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"items": values})
}
