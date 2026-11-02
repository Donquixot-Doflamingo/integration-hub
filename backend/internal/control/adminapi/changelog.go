package adminapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Server) registerChangeLog() {
	s.handle("listChangeLog", RoleViewer, false, "GET /change-log", s.listChangeLog)
	s.handle("getChangeLogEntry", RoleViewer, false, "GET /change-log/{change_id}", s.getChangeLogEntry)
}

type changeSummary struct {
	ChangeID        string    `json:"change_id"`
	At              time.Time `json:"at"`
	Actor           string    `json:"actor"`
	ActorRoles      []string  `json:"actor_roles"`
	Action          string    `json:"action"`
	ObjectType      string    `json:"object_type"`
	ObjectID        string    `json:"object_id"`
	Reason          string    `json:"reason"`
	SnapshotVersion *int64    `json:"snapshot_version"`
}

// page reads the limit (1..200, default 50) and the opaque cursor of a list operation.
func page(r *http.Request) (limit int, cursor []string, err error) {
	limit = 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit < 1 || limit > 200 {
			return 0, nil, badRequest("", "limit must be 1..200")
		}
	}
	if c := r.URL.Query().Get("cursor"); c != "" {
		b, err := base64.RawURLEncoding.DecodeString(c)
		if err != nil {
			return 0, nil, badRequest("", "cursor is not a cursor of this list")
		}
		cursor = strings.Split(string(b), "\x00")
	}
	return limit, cursor, nil
}

// nextCursor encodes the sort key of the last item of a full page.
func nextCursor(parts ...string) *string {
	c := base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, "\x00")))
	return &c
}

// timeRange reads the from (inclusive) and to (exclusive) query bounds.
func timeRange(r *http.Request) (from, to *time.Time, err error) {
	for _, b := range []struct {
		name string
		dst  **time.Time
	}{{"from", &from}, {"to", &to}} {
		if v := r.URL.Query().Get(b.name); v != "" {
			t, perr := time.Parse(time.RFC3339, v)
			if perr != nil {
				return nil, nil, badRequest("", b.name+" must be an RFC 3339 timestamp")
			}
			*b.dst = &t
		}
	}
	return from, to, nil
}

func (s *Server) listChangeLog(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := page(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	from, to, err := timeRange(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	var where []string
	var args []any
	arg := func(v any) string { args = append(args, v); return "$" + strconv.Itoa(len(args)) }
	for _, f := range []string{"actor", "object_type", "object_id", "action"} {
		if v := q.Get(f); v != "" {
			where = append(where, f+" = "+arg(v))
		}
	}
	if from != nil {
		where = append(where, "at >= "+arg(*from))
	}
	if to != nil {
		where = append(where, "at < "+arg(*to))
	}
	if len(cursor) == 2 {
		at, perr := time.Parse(time.RFC3339Nano, cursor[0])
		if perr != nil {
			s.fail(w, r, badRequest("", "cursor is not a cursor of this list"))
			return
		}
		where = append(where, "(at, change_id) < ("+arg(at)+", "+arg(cursor[1])+")")
	}
	sql := `SELECT change_id, at, actor, actor_roles, action, object_type, object_id, reason, snapshot_version FROM hub.change_log`
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	sql += " ORDER BY at DESC, change_id DESC LIMIT " + strconv.Itoa(limit+1)
	if s.d.Pool == nil {
		s.fail(w, r, errors.New("no database"))
		return
	}
	rows, err := s.d.Pool.Query(r.Context(), sql, args...)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[changeSummary])
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		next = nextCursor(last.At.Format(time.RFC3339Nano), last.ChangeID)
	}
	for i := range items {
		items[i].At = items[i].At.UTC()
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) getChangeLogEntry(w http.ResponseWriter, r *http.Request) {
	if s.d.Pool == nil {
		s.fail(w, r, errors.New("no database"))
		return
	}
	var e changeSummary
	var before, after []byte
	err := s.d.Pool.QueryRow(r.Context(), `SELECT change_id, at, actor, actor_roles, action, object_type, object_id, reason, snapshot_version, before, after
FROM hub.change_log WHERE change_id = $1`, r.PathValue("change_id")).Scan(&e.ChangeID, &e.At, &e.Actor, &e.ActorRoles, &e.Action, &e.ObjectType,
		&e.ObjectID, &e.Reason, &e.SnapshotVersion, &before, &after)
	if errors.Is(err, pgx.ErrNoRows) {
		s.fail(w, r, notFound("Change-log entry"))
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	e.At = e.At.UTC()
	writeJSON(w, http.StatusOK, struct {
		changeSummary
		Before json.RawMessage `json:"before"`
		After  json.RawMessage `json:"after"`
	}{e, jsonOrNull(before), jsonOrNull(after)})
}

func jsonOrNull(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return b
}
