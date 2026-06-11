package models

import "time"

// Settings holds all user-configurable values, persisted to bbolt.
type Settings struct {
	APIToken    string `json:"api_token"`
	ZoneID      string `json:"zone_id"`
	RecordID    string `json:"record_id"`
	RecordName  string `json:"record_name"`
	IntervalMin int    `json:"interval_min"` // 5, 15, 30, or 60
}

// LogEntry represents one execution of the DNS check cycle.
type LogEntry struct {
	ID        uint64    `json:"id"`
	Timestamp time.Time `json:"timestamp"` // always stored in UTC; formatted as UK on output
	Status    string    `json:"status"`    // "Success" | "No Change" | "Failed" | "Pending" | "Confirmed"
	OldIP     string    `json:"old_ip"`
	NewIP     string    `json:"new_ip"`
	Message   string    `json:"message"`
}
