// Command plex-file-manager is the Plex File Media Manager (v0.1).
//
// A single stdlib-only Go binary: an HTTP server + embedded web UI
// over the transport-agnostic core in internal/core. The core is
// Transport-separated (see HANDOFF): a future desktop shell of any kind could bind
// core.Service directly; this binary keeps serving Windows-as-localhost
// and Ultra.cc (systemd --user + nginx proxy) unchanged.
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"plexfilemanager/internal/core"
)

// indexHTML is the entire web UI, embedded so the binary is
// self-contained (no external assets, no CDN).
//
//go:embed web/index.html
var indexHTML []byte

// Favicon / app-icon set (v1.2.0), embedded like the page and
// served by the icon routes in server.go. The mark is its own
// amber tile (web/icons/favicon.svg is the source), so it reads
// identically on every UI theme and on light or dark browser
// chrome — the icons are theme-independent by design.
//
//go:embed web/icons/favicon.ico
var iconICO []byte

//go:embed web/icons/favicon.svg
var iconSVG []byte

//go:embed web/icons/favicon-32x32.png
var iconPNG32 []byte

//go:embed web/icons/apple-touch-icon.png
var iconAppleTouch []byte

//go:embed web/icons/android-chrome-192x192.png
var iconPNG192 []byte

//go:embed web/icons/android-chrome-512x512.png
var iconPNG512 []byte

// defaultDataDir picks an OS-appropriate per-user data directory.
func defaultDataDir() string {
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("LOCALAPPDATA"); appData != "" {
			return filepath.Join(appData, "PlexFileManager")
		}
	}
	home, err := os.UserHomeDir()
	if err == nil {
		return filepath.Join(home, ".local", "share", "plex-file-manager")
	}
	return "./data"
}

// openBrowser tries to open the UI in the default browser. It is
// OS-gated and best-effort: on headless Linux (Ultra) it is skipped.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		if _, err := exec.LookPath("xdg-open"); err != nil {
			return // headless: nothing sensible to open
		}
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// exitCodeForScan maps a finished one-shot scan to the CLI
// exit code (FEATURE-PLAN §7 — the table is the contract):
//
//	0  the scan completed
//	1  the scan itself failed
//	2  configuration error — no roots configured, or the data
//	   dir is locked by a running instance
//	3  only with -fail-on-findings: the scan completed AND
//	   findings the user asked to gate on are present
//	   (duplicate reclaimable bytes > 0 or health findings > 0)
func exitCodeForScan(scanErr string, sum core.ScanSummary, failOnFindings bool) int {
	if scanErr != "" {
		return 1
	}
	if failOnFindings && (sum.DuplicateReclaimable > 0 || sum.HealthFindings > 0) {
		return 3
	}
	return 0
}

// runScanOnce is the -scan-once mode: one headless scan through
// the shared StartScan path, progress lines as it runs, an
// optional report file, and the exit-code table above. No
// server, no browser, nothing left running.
func runScanOnce(svc *core.Service, reportPath string, failOnFindings bool) int {
	// Machine-clean stdout rule: when the report goes to stdout
	// ("-report -"), progress lines move to stderr so stdout is
	// pure report.
	progress := os.Stdout
	if reportPath == "-" {
		progress = os.Stderr
	}
	cfg := svc.GetConfig()
	if len(cfg.Roots) == 0 {
		fmt.Fprintln(os.Stderr, "no scan roots configured — add roots in the web UI or config.json first")
		return 2
	}
	if _, err := svc.StartScanTriggered(nil, "cli"); err != nil {
		fmt.Fprintf(os.Stderr, "scan did not start: %v\n", err)
		return 2
	}
	lastPhase, lastDone := "", -1
	var finalState core.StateInfo
	for {
		st := svc.State()
		finalState = st
		if st.Scan.Phase != lastPhase || st.Scan.Done != lastDone {
			lastPhase, lastDone = st.Scan.Phase, st.Scan.Done
			if st.Scan.Total > 0 {
				fmt.Fprintf(progress, "%s: %d/%d\n", st.Scan.Phase, st.Scan.Done, st.Scan.Total)
			} else if st.Scan.Phase != "" {
				fmt.Fprintf(progress, "%s\n", st.Scan.Phase)
			}
		}
		if !st.Scan.Running {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	sum := finalState.Scan.Summary
	fmt.Fprintf(progress, "scan complete: %d media · %d duplicate groups · %d subtitle findings · %d health findings\n",
		sum.MediaCount, sum.DuplicateGroups, sum.SubtitleFindings, sum.HealthFindings)
	if reportPath != "" {
		var data []byte
		var rerr error
		if strings.HasSuffix(strings.ToLower(reportPath), ".html") {
			data, rerr = svc.ReportHTML()
		} else {
			rc := svc.Report()
			data, rerr = json.MarshalIndent(rc, "", "  ")
		}
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "report: %v\n", rerr)
			return 1
		}
		if reportPath == "-" {
			os.Stdout.Write(data)
			fmt.Println()
		} else if err := os.WriteFile(reportPath, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "report: %v\n", err)
			return 1
		} else {
			fmt.Fprintf(progress, "report written to %s\n", reportPath)
		}
	}
	return exitCodeForScan(finalState.Scan.LastError, sum, failOnFindings)
}

// main parses flags, builds the core service, and serves HTTP —
// or, with -scan-once, runs one headless scan and exits.
func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "listen address (host:port)")
	dataDir := flag.String("data", "", "data directory (default: OS per-user location)")
	baseURL := flag.String("baseurl", "", "path prefix when behind a reverse proxy, e.g. /plex-file-manager")
	noBrowser := flag.Bool("no-browser", false, "do not try to open a browser on start")
	scanOnce := flag.Bool("scan-once", false, "run one scan headless and exit (see the exit-code table in the docs)")
	reportPath := flag.String("report", "", "with -scan-once: write the report card to this path (.html for HTML, otherwise JSON; \"-\" for stdout)")
	failOnFindings := flag.Bool("fail-on-findings", false, "with -scan-once: exit 3 when the scan completes with gated findings present")
	flag.Parse()

	dd := *dataDir
	if dd == "" {
		dd = defaultDataDir()
	}
	// v1.0.0 single-instance lock: exactly one process owns a
	// data dir (its stores are single-writer). A locked dir is a
	// configuration error (exit 2) for both the server and the
	// CLI — the refusal message names the lock and the way out.
	if err := os.MkdirAll(dd, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "data dir: %v\n", err)
		os.Exit(2)
	}
	lock, err := core.AcquireDataLock(dd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "WellKeptLib: %v\n", err)
		os.Exit(2)
	}
	defer lock.Release()
	svc, err := core.NewService(dd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "data dir: %v\n", err)
		os.Exit(2)
	}
	if *scanOnce {
		// os.Exit skips deferred calls — release the lock
		// explicitly so a completed one-shot never leaves a
		// stale app.lock behind for the next run to trip over.
		code := runScanOnce(svc, *reportPath, *failOnFindings)
		lock.Release()
		os.Exit(code)
	}
	// v1.1.1: on Windows, closing the console window (how a
	// double-clicked app is normally closed) terminates the
	// process without running the deferred lock release above,
	// leaving app.lock behind. Register a console control
	// handler that releases the lock on window close / logoff
	// / shutdown. No-op on other platforms.
	installConsoleLockRelease(lock)
	// The scheduler runs ONLY in server mode — a one-shot CLI
	// run must never leave a ticker behind (and must never fire
	// a second scan of its own).
	svc.StartScheduler()
	base := strings.TrimSuffix(*baseURL, "/")
	if base != "" && !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	srv := newServer(svc, indexHTML, base)
	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}
	url := "http://" + *addr + base + "/"
	fmt.Printf("WellKeptLib v%s\n", core.Version)
	fmt.Printf("Data dir: %s\n", dd)
	fmt.Printf("Listening on %s\n", url)
	if !*noBrowser {
		go func() {
			time.Sleep(400 * time.Millisecond)
			openBrowser(url)
		}()
	}
	log.Fatal(httpSrv.ListenAndServe())
}
