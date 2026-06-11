// Package monitor runs the background DNS-check ticker.
package monitor

import (
	"context"
	"log"
	"sync"
	"time"

	"autodns/internal/cloudflare"
	"autodns/internal/ip"
	"autodns/internal/store"
	"autodns/internal/store/models"
)

// Monitor owns the ticker goroutine and the last-known IP cache.
type Monitor struct {
	store       *store.Store
	mu          sync.RWMutex
	lastIP      string
	lastChecked time.Time

	// triggerCh allows callers to request an immediate check without stopping
	// and restarting the goroutine, which avoids a window with no goroutine running.
	triggerCh chan struct{}

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates a Monitor, seeding the last-known IP from the most recent
// successful log entry. This prevents a spurious Cloudflare update on restart
// when the IP has not actually changed since the last run.
func New(st *store.Store) *Monitor {
	m := &Monitor{
		store:     st,
		triggerCh: make(chan struct{}, 1),
	}
	if seeded, err := st.GetLastSuccessIP(); err == nil && seeded != "" {
		m.lastIP = seeded
	}
	return m
}

// LastIP returns the most recently confirmed public IP (empty if never confirmed).
func (m *Monitor) LastIP() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastIP
}

// LastChecked returns the UTC time of the most recent IP check attempt.
func (m *Monitor) LastChecked() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastChecked
}

// PendingConfirm is retained for API compatibility.
func (m *Monitor) PendingConfirm() (pending bool, pendingIP string) {
	return false, ""
}

// Confirm is retained for API compatibility.
func (m *Monitor) Confirm() {
	// no-op
}

// Trigger signals the monitor to run a check immediately without restarting
// the goroutine. The buffered channel means duplicate signals are dropped safely.
func (m *Monitor) Trigger() {
	select {
	case m.triggerCh <- struct{}{}:
	default: // already queued
	}
}

// Start launches the background ticker using the interval stored in settings.
// It is safe to call Start again after Stop to restart with a new interval.
func (m *Monitor) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel

	m.wg.Add(1)
	go m.run(ctx)
}

// Stop gracefully halts the background goroutine and waits for it to exit.
func (m *Monitor) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.wg.Wait()
}

// run is the long-lived goroutine. It ticks immediately on start, then waits
// for either the configured interval, an external trigger, or cancellation.
func (m *Monitor) run(ctx context.Context) {
	defer m.wg.Done()

	// Run once immediately so the dashboard shows a result quickly.
	m.tick(ctx)

	for {
		cfg, err := m.store.GetSettings()
		if err != nil {
			log.Printf("monitor: failed to load settings: %v", err)
			cfg.IntervalMin = 15
		}
		if cfg.IntervalMin <= 0 {
			cfg.IntervalMin = 15
		}

		timer := time.NewTimer(time.Duration(cfg.IntervalMin) * time.Minute)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-m.triggerCh:
			timer.Stop()
			m.tick(ctx)
		case <-timer.C:
			m.tick(ctx)
		}
	}
}

// tick performs one full check: resolve WAN IP, compare to baseline, and
// update the Cloudflare DNS record if the IP has changed.
func (m *Monitor) tick(ctx context.Context) {
	cfg, err := m.store.GetSettings()
	if err != nil {
		m.appendLog(models.LogEntry{
			Timestamp: time.Now().UTC(),
			Status:    "Failed",
			Message:   "could not load settings: " + err.Error(),
		})
		return
	}

	ipCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	currentIP, err := ip.GetPublicIP(ipCtx)
	if err != nil {
		m.appendLog(models.LogEntry{
			Timestamp: time.Now().UTC(),
			Status:    "Failed",
			Message:   "IP lookup failed: " + err.Error(),
		})
		return
	}

	m.mu.Lock()
	now := time.Now().UTC()
	m.lastChecked = now
	oldIP := m.lastIP
	m.mu.Unlock()

	// No change — nothing to do.
	if oldIP != "" && currentIP == oldIP {
		m.appendLog(models.LogEntry{
			Timestamp: now,
			Status:    "No Change",
			OldIP:     oldIP,
			NewIP:     currentIP,
			Message:   "IP unchanged",
		})
		return
	}

	// Settings incomplete — cannot update Cloudflare.
	if cfg.APIToken == "" || cfg.ZoneID == "" || (cfg.RecordID == "" && cfg.RecordName == "") {
		m.appendLog(models.LogEntry{
			Timestamp: now,
			Status:    "Failed",
			OldIP:     oldIP,
			NewIP:     currentIP,
			Message:   "IP changed but Cloudflare settings are incomplete",
		})
		return
	}

	// Push update to Cloudflare.
	cf := cloudflare.NewClient(cfg.APIToken)
	cfCtx, cfCancel := context.WithTimeout(ctx, 20*time.Second)
	defer cfCancel()

	recordID := cfg.RecordID
	recordName := cfg.RecordName
	proxied := true

	// If only record name is configured, look up the record id/proxy mode first.
	if recordID == "" {
		record, err := cf.FindRecord(cfCtx, cfg.ZoneID, cfg.RecordName)
		if err != nil {
			m.appendLog(models.LogEntry{
				Timestamp: now,
				Status:    "Failed",
				OldIP:     oldIP,
				NewIP:     currentIP,
				Message:   "find record: " + err.Error(),
			})
			return
		}
		recordID = record.ID
		recordName = record.Name
		proxied = record.Proxied
	}

	cfMsg, err := cf.UpdateRecord(cfCtx, cfg.ZoneID, recordID, recordName, currentIP, proxied)
	if err != nil {
		m.appendLog(models.LogEntry{
			Timestamp: now,
			Status:    "Failed",
			OldIP:     oldIP,
			NewIP:     currentIP,
			Message:   "update record: " + err.Error(),
		})
		return
	}

	m.mu.Lock()
	m.lastIP = currentIP
	m.mu.Unlock()

	m.appendLog(models.LogEntry{
		Timestamp: now,
		Status:    "Success",
		OldIP:     oldIP,
		NewIP:     currentIP,
		Message:   cfMsg,
	})
}

func (m *Monitor) appendLog(entry models.LogEntry) {
	if err := m.store.AppendLog(entry); err != nil {
		log.Printf("monitor: failed to append log: %v", err)
	}
}
