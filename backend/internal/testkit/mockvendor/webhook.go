package mockvendor

import "net/http"

// triggerWebhook sends an inbound webhook to the Hub (HUB-030).
func (s *Server) triggerWebhook(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "INTERNAL_ERROR", "webhooks arrive in HUB-030", nil)
}
