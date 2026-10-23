package core

// webhook.go — the scan-finished webhook (v1.0.0, FEATURE-PLAN
// §6).
//
// When enabled, scan completion POSTs a small JSON summary to a
// user-configured URL — generic (the payload below as-is),
// Discord ({"content": ...}), or ntfy (plain-text body with
// title/tag headers). Failed scans announce themselves too,
// distinctly labeled (scan_failed); cancelled scans stay silent.
//
// Hard rules (from the plan):
//   - Fire-and-forget: delivery runs on its own goroutine with
//     a 10-second client ceiling. A dead endpoint can NEVER fail
//     or delay the scan's completion path — the scan code calls
//     fireWebhook and does not wait for anything.
//   - Delivery errors go to the server log + the in-memory
//     status the diagnostics view shows — nowhere user-blocking.
//   - The payload carries COUNTS AND A SUMMARY ONLY: no
//     credentials, no file paths, no per-file listings. The
//     webhook URL itself is a credential of sorts (Discord
//     webhook URLs embed a token) — it is never echoed into the
//     payload, the status, or any API response.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// webhookSummary is the event's count block (the generic
// payload's "summary"). Deliberately counts-only.
type webhookSummary struct {
	Media            int   `json:"media"`
	DuplicateGroups  int   `json:"duplicate_groups"`
	DuplicateWasted  int64 `json:"duplicate_wasted_bytes"`
	ReclaimableBytes int64 `json:"duplicate_reclaimable_bytes"`
	SubtitleFindings int   `json:"subtitle_findings"`
	HealthFindings   int   `json:"health_findings"`
	GapEpisodes      int   `json:"gap_episodes"`
}

// webhookPayload is the generic flavor's body.
type webhookPayload struct {
	App     string         `json:"app"` // always "WellKeptLib"
	Version string         `json:"version"`
	Event   string         `json:"event"` // scan_finished | scan_failed
	At      string         `json:"at"`
	Summary webhookSummary `json:"summary"`
}

// webhookStatus is the last delivery attempt's outcome (kept in
// memory, surfaced by diagnostics — no URL, ever).
type webhookStatus struct {
	At    string `json:"at,omitempty"`
	Event string `json:"event,omitempty"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// webhookHTTPClient is the delivery client (a var so tests can
// point it at a mock transport; the timeout is the contract).
var webhookHTTPClient = &http.Client{Timeout: 10 * time.Second}

// humanBytes formats a byte count for the one-line flavor
// texts (core has no shared formatter; the UI does its own).
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// webhookText renders the one-line human summary the Discord /
// ntfy flavors carry.
func webhookText(event string, sum ScanSummary) string {
	verb := "finished"
	if event == "scan_failed" {
		verb = "FAILED"
	}
	return fmt.Sprintf("WellKeptLib scan %s — %d media · %d duplicate groups (%s reclaimable) · %d subtitle findings · %d health findings",
		verb, sum.MediaCount, sum.DuplicateGroups, humanBytes(sum.DuplicateReclaimable), sum.SubtitleFindings, sum.HealthFindings)
}

// buildWebhookRequest constructs the flavor-appropriate request
// for cfg. Pure given (cfg, event, summary) so the payload
// shapes are unit-pinned without a network.
func buildWebhookRequest(cfg Config, event string, sum ScanSummary, now time.Time) (*http.Request, error) {
	at := now.UTC().Format(time.RFC3339)
	summary := webhookSummary{
		Media:            sum.MediaCount,
		DuplicateGroups:  sum.DuplicateGroups,
		DuplicateWasted:  sum.DuplicateWasted,
		ReclaimableBytes: sum.DuplicateReclaimable,
		SubtitleFindings: sum.SubtitleFindings,
		HealthFindings:   sum.HealthFindings,
		GapEpisodes:      sum.GapEpisodes,
	}
	switch cfg.WebhookFlavor {
	case "discord":
		body, err := json.Marshal(map[string]string{"content": webhookText(event, sum)})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodPost, cfg.WebhookURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	case "ntfy":
		req, err := http.NewRequest(http.MethodPost, cfg.WebhookURL, strings.NewReader(webhookText(event, sum)))
		if err != nil {
			return nil, err
		}
		title := "WellKeptLib scan finished"
		if event == "scan_failed" {
			title = "WellKeptLib scan FAILED"
		}
		req.Header.Set("Title", title)
		req.Header.Set("Tags", "clapper,bell")
		return req, nil
	default: // generic
		body, err := json.Marshal(webhookPayload{App: "WellKeptLib", Version: Version, Event: event, At: at, Summary: summary})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodPost, cfg.WebhookURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}
}

// deliverWebhook performs one delivery attempt and records the
// outcome. It never panics and its error affects nothing but the
// log + status — the fire-and-forget contract's synchronous
// half, directly unit-tested.
func (s *Service) deliverWebhook(event string, sum ScanSummary) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	record := func(ok bool, errText string) {
		st := webhookStatus{At: time.Now().UTC().Format(time.RFC3339), Event: event, OK: ok, Error: errText}
		s.mu.Lock()
		s.webhookLast = st
		s.mu.Unlock()
		if !ok {
			log.Printf("wellkeptlib: webhook %s delivery failed: %s", event, errText)
		}
	}
	if !cfg.WebhookEnabled || strings.TrimSpace(cfg.WebhookURL) == "" {
		return
	}
	req, err := buildWebhookRequest(*cfg, event, sum, time.Now())
	if err != nil {
		record(false, err.Error())
		return
	}
	resp, err := webhookHTTPClient.Do(req)
	if err != nil {
		record(false, err.Error())
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		record(false, fmt.Sprintf("endpoint returned HTTP %d", resp.StatusCode))
		return
	}
	record(true, "")
}

// fireWebhook is the scan pipeline's hook: enabled check +
// goroutine. Returns immediately, always.
func (s *Service) fireWebhook(event string, sum ScanSummary) {
	s.mu.Lock()
	enabled := s.cfg.WebhookEnabled && strings.TrimSpace(s.cfg.WebhookURL) != ""
	s.mu.Unlock()
	if !enabled {
		return
	}
	go s.deliverWebhook(event, sum)
}

// WebhookLast returns the last delivery attempt's status for
// the diagnostics view (no URL, no payload — outcome only).
func (s *Service) WebhookLast() webhookStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.webhookLast
}
