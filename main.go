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

// main parses flags, builds the core service, and serves HTTP.
func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "listen address (host:port)")
	dataDir := flag.String("data", "", "data directory (default: OS per-user location)")
	baseURL := flag.String("baseurl", "", "path prefix when behind a reverse proxy, e.g. /plex-file-manager")
	noBrowser := flag.Bool("no-browser", false, "do not try to open a browser on start")
	flag.Parse()

	dd := *dataDir
	if dd == "" {
		dd = defaultDataDir()
	}
	svc, err := core.NewService(dd)
	if err != nil {
		log.Fatalf("data dir: %v", err)
	}
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
