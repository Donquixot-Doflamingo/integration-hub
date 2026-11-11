package mockisfc

import (
	"encoding/json"
	"net/http"
)

// control registers /_mock/*: the same state changes as the Go API, for cmd/mockisfc and for
// driving a running mock from scripts. These routes need no credentials; the mock is not a
// security boundary.
func (s *Server) control(mux *http.ServeMux) {
	body := func(w http.ResponseWriter, r *http.Request, v any) bool {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v); err != nil {
			writeErr(w, http.StatusBadRequest, "MOCK_BAD_BODY", "invalid JSON body")
			return false
		}
		return true
	}
	mux.HandleFunc("POST /_mock/tenants", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Key, Name string }
		if body(w, r, &b) {
			writeJSON(w, http.StatusCreated, map[string]string{"id": s.AddTenant(b.Key, b.Name)})
		}
	})
	mux.HandleFunc("POST /_mock/tenants/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Status string }
		if body(w, r, &b) {
			s.SetTenantStatus(r.PathValue("id"), b.Status)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("POST /_mock/clients", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			ID, Secret string
			Type       string `json:"type"`
		}
		if body(w, r, &b) {
			s.AddClient(b.ID, b.Secret, b.Type)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("POST /_mock/clients/{id}/tenants", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Tenants []string }
		if body(w, r, &b) {
			s.SetClientTenants(r.PathValue("id"), b.Tenants...)
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("POST /_mock/keys", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Owner, Caller string
			Permissions   []string
		}
		if body(w, r, &b) {
			id, key := s.IssueKey(b.Owner, b.Caller, b.Permissions...)
			writeJSON(w, http.StatusCreated, map[string]string{"id": id, "key": key})
		}
	})
	mux.HandleFunc("POST /_mock/keys/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		s.RevokeKey(r.PathValue("id"))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /_mock/down", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Down bool }
		if body(w, r, &b) {
			s.SetDown(b.Down)
			w.WriteHeader(http.StatusNoContent)
		}
	})
}
