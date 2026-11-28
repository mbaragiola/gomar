package main

import (
	"net/http"
	"sync/atomic"
)

const (
	publicReservationConcurrency = 2
	privateImageConcurrency      = 1
)

// Reject extra work before reading the body or starting the expensive phase.
func admitRequestWork(w http.ResponseWriter, active *atomic.Int32, limit int32) (func(), error) {
	if active.Add(1) > limit {
		active.Add(-1)
		w.Header().Set("Retry-After", "2")
		return nil, problem{503, "El servicio está ocupado. Intentá de nuevo en unos segundos."}
	}
	return func() { active.Add(-1) }, nil
}
