// Package api exposes the REST endpoints consumed by the SPA frontend.
package api

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"autodns/internal/monitor"
	"autodns/internal/store"
)

const londonTZ = "Europe/London"

// Handler wires together the store, monitor, and HTTP mux.
type Handler struct {
	store   *store.Store
	monitor *monitor.Monitor
	mux     *http.ServeMux
	authKey string
}

// New creates a Handler and registers all API routes onto the provided mux.
func New(st *store.Store, mon *monitor.Monitor, mux *http.ServeMux, authKey string) *Handler {
	h := &Handler{store: st, monitor: mon, mux: mux, authKey: authKey}
	h.registerRoutes()
	return h
}

var zoneIDRe = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)
var recordIDRe = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)
var recordNameRe = regexp.MustCompile(`^[A-Za-z0-9*.-]{1,253}$`)

func (h *Handler) registerRoutes() {
	h.mux.HandleFunc("GET /api/status", h.getStatus)
	h.mux.HandleFunc("GET /api/settings", h.getSettings)
	h.mux.HandleFunc("POST /api/settings", h.postSettings)
	h.mux.HandleFunc("GET /api/logs", h.getLogs)
	h.mux.HandleFunc("POST /api/check", h.triggerCheck)
	h.mux.HandleFunc("POST /api/confirm", h.confirmIP)
}

// statusResponse is the shape returned by GET /api/status.
type statusResponse struct {
	LastIP         string `json:"last_ip"`
	UpdatedAt      string `json:"updated_at"`
	PendingConfirm bool   `json:"pending_confirm"`
	PendingIP      string `json:"pending_ip,omitempty"`
}

func (h *Handler) getStatus(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}

	pending, pendingIP := h.monitor.PendingConfirm()
	lastChecked := h.monitor.LastChecked()

	var updatedAt string
	if !lastChecked.IsZero() {
		updatedAt = formatUKTime(lastChecked)
	}

	respond(w, http.StatusOK, statusResponse{
		LastIP:         h.monitor.LastIP(),
		UpdatedAt:      updatedAt,
		PendingConfirm: pending,
		PendingIP:      pendingIP,
	})
}

// settingsResponse is the shape returned by GET /api/settings.
// The raw API token is never sent to the client; api_token_set signals
// whether one is stored so the frontend can show an appropriate placeholder.
type settingsResponse struct {
	APITokenSet bool   `json:"api_token_set"`
	ZoneID      string `json:"zone_id"`
	RecordID    string `json:"record_id"`
	RecordName  string `json:"record_name"`
	IntervalMin int    `json:"interval_min"`
}

func (h *Handler) getSettings(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}

	cfg, err := h.store.GetSettings()
	if err != nil {
		log.Printf("api: reading settings: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	respond(w, http.StatusOK, settingsResponse{
		APITokenSet: cfg.APIToken != "",
		ZoneID:      cfg.ZoneID,
		RecordID:    cfg.RecordID,
		RecordName:  cfg.RecordName,
		IntervalMin: cfg.IntervalMin,
	})
}

func (h *Handler) postSettings(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 8<<10) // 8 KiB
	defer r.Body.Close()

	var incoming struct {
		APIToken    string `json:"api_token"`
		ZoneID      string `json:"zone_id"`
		RecordID    string `json:"record_id"`
		RecordName  string `json:"record_name"`
		IntervalMin int    `json:"interval_min"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&incoming); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		respondError(w, http.StatusBadRequest, "invalid JSON: single object expected")
		return
	}

	incoming.APIToken = strings.TrimSpace(incoming.APIToken)
	incoming.ZoneID = strings.TrimSpace(incoming.ZoneID)
	incoming.RecordID = strings.TrimSpace(incoming.RecordID)
	incoming.RecordName = strings.TrimSpace(strings.ToLower(incoming.RecordName))

	// Validate interval.
	valid := map[int]bool{5: true, 15: true, 30: true, 60: true}
	if !valid[incoming.IntervalMin] {
		respondError(w, http.StatusBadRequest, "interval_min must be 5, 15, 30, or 60")
		return
	}
	if incoming.ZoneID != "" && !zoneIDRe.MatchString(incoming.ZoneID) {
		respondError(w, http.StatusBadRequest, "zone_id must be a 32-char hex string")
		return
	}
	if incoming.RecordID != "" && !recordIDRe.MatchString(incoming.RecordID) {
		respondError(w, http.StatusBadRequest, "record_id must be a 32-char hex string")
		return
	}
	if incoming.RecordName != "" {
		if strings.Contains(incoming.RecordName, "..") || strings.HasPrefix(incoming.RecordName, ".") || strings.HasSuffix(incoming.RecordName, ".") {
			respondError(w, http.StatusBadRequest, "record_name must be a valid DNS name")
			return
		}
		if !recordNameRe.MatchString(incoming.RecordName) {
			respondError(w, http.StatusBadRequest, "record_name contains invalid characters")
			return
		}
	}
	if len(incoming.APIToken) > 512 {
		respondError(w, http.StatusBadRequest, "api_token is too long")
		return
	}
	if incoming.RecordID == "" && incoming.RecordName == "" {
		respondError(w, http.StatusBadRequest, "set either record_id or record_name")
		return
	}

	// Load existing settings so we can preserve the token if none was submitted.
	cfg, err := h.store.GetSettings()
	if err != nil {
		log.Printf("api: reading existing settings: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// Only overwrite the stored token when the client explicitly provides one.
	if incoming.APIToken != "" {
		cfg.APIToken = incoming.APIToken
	}
	cfg.ZoneID = incoming.ZoneID
	cfg.RecordID = incoming.RecordID
	cfg.RecordName = incoming.RecordName
	cfg.IntervalMin = incoming.IntervalMin

	if err := h.store.SaveSettings(cfg); err != nil {
		log.Printf("api: saving settings: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	// Trigger an immediate check so the new interval takes effect without
	// stopping and restarting the goroutine.
	h.monitor.Trigger()

	respond(w, http.StatusOK, map[string]string{"status": "saved"})
}

// logEntryView is the wire format for a log entry, with UK-formatted timestamp.
type logEntryView struct {
	ID        uint64 `json:"id"`
	Timestamp string `json:"timestamp"`
	Status    string `json:"status"`
	OldIP     string `json:"old_ip"`
	NewIP     string `json:"new_ip"`
	Message   string `json:"message"`
}

func (h *Handler) getLogs(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}

	entries, err := h.store.GetLogs()
	if err != nil {
		log.Printf("api: reading logs: %v", err)
		respondError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	views := make([]logEntryView, 0, len(entries))
	for _, e := range entries {
		views = append(views, logEntryView{
			ID:        e.ID,
			Timestamp: formatUKTime(e.Timestamp),
			Status:    e.Status,
			OldIP:     e.OldIP,
			NewIP:     e.NewIP,
			Message:   e.Message,
		})
	}

	respond(w, http.StatusOK, views)
}

func (h *Handler) triggerCheck(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}

	h.monitor.Trigger()
	respond(w, http.StatusOK, map[string]string{"status": "check triggered"})
}

func (h *Handler) confirmIP(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}

	pending, pendingIP := h.monitor.PendingConfirm()
	if !pending {
		respondError(w, http.StatusBadRequest, "no pending IP confirmation")
		return
	}
	h.monitor.Confirm()
	respond(w, http.StatusOK, map[string]string{"status": "confirmed", "ip": pendingIP})
}

// --- helpers ---

func respond(w http.ResponseWriter, code int, body interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("api: encoding response: %v", err)
	}
}

func respondError(w http.ResponseWriter, code int, msg string) {
	respond(w, code, map[string]string{"error": msg})
}

// formatUKTime formats a time value as DD/MM/YYYY HH:mm:ss in Europe/London.
func formatUKTime(t time.Time) string {
	loc, err := time.LoadLocation(londonTZ)
	if err != nil {
		// Fallback to UTC if tz data is unavailable.
		loc = time.UTC
	}
	return t.In(loc).Format("02/01/2006 15:04:05")
}

// requireAuth checks an optional static API token.
// If no token is configured, endpoints are left open for local-only deployments.
func (h *Handler) requireAuth(w http.ResponseWriter, r *http.Request) bool {
	if h.authKey == "" {
		return true
	}

	presented := strings.TrimSpace(r.Header.Get("X-AutoDNS-Token"))
	if presented == "" {
		authz := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(authz), "bearer ") {
			presented = strings.TrimSpace(authz[7:])
		}
	}

	if presented == "" {
		respondError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}

	if subtle.ConstantTimeCompare([]byte(presented), []byte(h.authKey)) != 1 {
		respondError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}

	return true
}
