# WellKeptLib

**A file-level keeper for your media library.** WellKeptLib scans
the folders your media actually lives in and tells you what's
wrong with the *files* — then helps you fix the layout. It works
on disk, not through a media-server API: every core tool works
with no Plex connection at all. (An optional Plex sign-in exists,
and only saves you typing folder paths.)

One static binary. No runtime, no database server, no Docker.
It runs as a small local web app: a service on your server, a
double-click `.exe` on Windows.

- **Version:** 1.1.1
- **License:** MIT (see [LICENSE](LICENSE))
- **Platforms:** Linux x86-64, Linux ARM64, Windows x86-64
- **Dependencies:** Go standard library only (no third-party
  packages); ffmpeg/ffprobe at runtime for media probing

## What it does

- **Duplicates, by identity — not by bytes.** Finds the same
  movie or episode stored more than once even when the copies
  differ in codec, resolution, or size (a 1080p x264 and a 720p
  x265 of the same episode are one group). Editions (Extended,
  Director's Cut, …) are kept separate. Groups show total wasted
  space, per-copy resolution / codec / bitrate / HDR / duration,
  a suggested keeper — and guarded per-copy delete (never a
  group's last copy, always logged). **Hardlink-aware:**
  copies that are hardlinks to the same physical file are
  identified as such (device/inode captured during the scan),
  and the group shows *reclaimable* space — what deleting
  copies would actually free — next to the apparent figure;
  when a file's identity can't be determined, the figure is
  honestly labeled an estimate.
- **Subtitle truth-checking.** Subtitle files and embedded
  tracks are checked against their *text*, not their labels: a
  track tagged English whose cues are actually Spanish is
  flagged, with the detected language, a confidence value, and
  sample lines as evidence. Image-based tracks (PGS/VobSub)
  are reported as "cannot verify", never guessed. The
  embedded-track audit can be switched off in Settings.
  A guided **subtitle wizard** works through the findings:
  dismiss ones you've reviewed (dismissals are bound to the
  file's fingerprint, so a changed file resurfaces), relabel
  a mislabeled sidecar (with undo), delete a bad sidecar
  (guarded), and fill gaps by downloading from OpenSubtitles
  (see below).
- **Library health.** Empty and truncated files, unparseable
  containers, missing video streams, broken subtitles, and
  leftover download artefacts (`.part`, `.crdownload`, …) —
  each finding carries the probe evidence that caused it.
  Findings have guarded per-file actions (open location,
  delete) with an audit log.
- **Inventory by size.** Movies, series, and seasons sorted
  by size, per-root totals, biggest files overall.
- **Missing episodes.** A gaps view lists episodes that are
  absent between the ones you have, per series and season.
- **Subtitle coverage.** Per-show / per-library coverage for
  the languages you want (a persisted wanted-languages list):
  which episodes have which languages, and which are missing.
- **Folder hygiene.** Orphan sidecar files (subtitles, .nfo,
  artwork with no video beside them) and empty folders,
  listed for review.
- **Report card + export.** A Report tab aggregates the whole
  library (summary, duplicates, findings, coverage, gaps),
  downloadable as a sectioned CSV or a self-contained HTML
  page (no scripts, no external references).
- **Scan history.** Every completed scan is recorded (up to
  50) with a delta against the previous one — added, removed,
  and changed files — with missing roots handled honestly
  (a root that wasn't scanned is never reported as mass
  deletions).
- **Scheduled scans + webhook.** Optional in-process
  scheduled rescans (every N hours, or daily at a set time;
  the app must be running) and an optional scan-finished
  webhook (generic JSON, Discord, or ntfy flavor) whose
  payload carries counts only — never paths or credentials.
  Delivery is fire-and-forget: a dead webhook can never fail
  or delay a scan.
- **One-shot CLI mode.** `-scan-once` runs a scan from the
  command line (optionally writing a report file), with
  script-friendly exit codes: 0 = completed, 1 = scan failed,
  2 = configuration error (including "another instance is
  using this data folder"), 3 = only with `-fail-on-findings`,
  when a completed scan found reclaimable duplicates or
  health problems.
- **OpenSubtitles downloads (optional, your own key).** The
  wizard can search OpenSubtitles and download a missing
  subtitle beside the media file (never overwriting; the
  downloaded file is language-checked by the app's own
  detector before placement and the verdict is shown).
  Bring your own free OpenSubtitles API key, stored locally
  (see Transparency). Downloads use your own account login
  and the quota the API itself reports — nothing is
  hardcoded. **Honest status: this client is verified
  against a mock of the documented API shapes, not yet
  against the live API** — see Known limitations.
- **Organize into Jellyfin folders.** Pick a messy folder and
  preview a full Jellyfin-layout plan (`Title (Year)/…`,
  `Series/Season 01/Series - S01E01 - Title.ext`) before
  anything moves. Apply re-validates the tree, writes an undo
  manifest *before* the first move, and Undo restores the exact
  original layout — weeks later, if you like. Unidentified
  files are left in place and listed with reasons; collisions
  are flagged, never overwritten.
- **…or build the library as hardlinks (new in 1.1.0).** A
  second Organize mode builds the same Jellyfin layout under
  a destination folder you choose as **hardlinks** to the
  untouched originals: your existing folders (and any seeding
  paths) stay exactly as they are, and the new library costs
  no extra disk space. The preview proves feasibility up
  front from the scan's device/inode data and sorts every
  file: links to create, already linked (re-running a build
  is a no-op), collisions (the keeper is picked by the same
  ranking as Duplicates — resolution → HDR → bitrate → size;
  copies whose durations suggest different cuts are left
  **unresolved**, never auto-linked), unidentified files,
  unlinkable files (cross-device or unknown identity, with
  the reason), and target conflicts — a real file already at
  a target is never overwritten or deleted. Apply writes its
  manifest first and creates directories + links only. Undo
  removes only links that still point at the recorded
  source; a target you replaced with a real file is skipped
  and reported. Keep the destination **outside** your scan
  roots (the UI warns when it isn't) so the linked copies
  don't show up as duplicates of themselves. Hardlinks need
  source and destination on the **same filesystem**, and
  deleting an original doesn't free space while a link to it
  remains — both stated in the UI.

Scanning is always read-only. The only features that touch
files are Organize (preview → apply → undo; the link mode
creates hardlinks in the destination folder only — sources
are never modified, and its undo verifies each link still
points at the recorded source before removing it), the
guarded per-file deletes on the Health and Duplicates tabs,
and the subtitle wizard's guarded actions (relabel with
undo, delete, and placing a downloaded subtitle beside its
media file — never overwriting). File-changing actions
validate against the current findings and write audit logs
in the data folder (`deleted-actions.json`,
`subtitle-actions.json`, `subtitle-manifests/`,
`link-manifests/`).

## Install

Get `WellKeptLib-1.1.1-public.zip` from the
[Releases](../../releases) page and extract it. The package
contains the three binaries, `install.sh`, and the two install
guides ([INSTALL-ULTRA.md](INSTALL-ULTRA.md),
[INSTALL-WINDOWS.md](INSTALL-WINDOWS.md)).

### Linux server (seedbox, home server, VPS)

```sh
# extract the release zip, then:
./install.sh
```

The installer asks for a port and a bind mode, installs the
binary to `~/bin`, writes a user systemd service, starts it,
and verifies the app actually answers before declaring success.

- **proxy mode** (default): listens on `127.0.0.1` only, for a
  reverse proxy (nginx/Caddy) in front — on Ultra.cc-shaped
  hosts (a `~/.apps/nginx/proxy.d` directory) it writes the
  nginx snippet for you; elsewhere it prints the snippet.
- **lan mode**: listens on the network directly. Read the
  security warning below first.

The same script **updates** an existing install (timestamped
binary + config backups, version check after the swap,
automatic rollback if the new binary doesn't come up) and
**uninstalls** with `./install.sh --uninstall` (your data
folder is kept unless you explicitly say otherwise). No sudo
is used anywhere. Manual steps, if you prefer them, are in
[INSTALL-ULTRA.md](INSTALL-ULTRA.md).

The final address on a proxied host is
`https://<your-slot-hostname>/plex-file-manager`. First run:
type your library folders into **Library roots** (one per
line), click **▶ Start scan**, and browse the tabs.

### Windows

1. Extract the ZIP to a folder you keep.
2. Double-click `plex-file-manager.exe` — a console window
   opens and stays open; that window is the app running.
   SmartScreen may warn (the exe is unsigned): **More info →
   Run anyway**.
3. Your browser opens at `http://127.0.0.1:8787/`. Add your
   library folders, click **▶ Start scan**.

Full click-by-click, including the ffmpeg fix and the
command-line flags (`-addr`, `-data`, `-baseurl`,
`-no-browser`), is in [INSTALL-WINDOWS.md](INSTALL-WINDOWS.md).

### Requirements

- **ffmpeg / ffprobe** on the machine (the app probes media
  with them). The installer offers a static build if they're
  missing; on Windows, dropping `ffprobe.exe`/`ffmpeg.exe`
  next to the app works too. Paths can be set in Settings —
  use full paths, `~` is not expanded there.
- A free port you control. On a seedbox, use one from your
  assigned range.

## ⚠ Security: there is no login

WellKeptLib has **no authentication of its own** — anyone who
can reach its port can use it, including Organize and the
guarded deletes. Run it behind a reverse proxy with basic
auth (a 3-step recipe is in
[INSTALL-ULTRA.md](INSTALL-ULTRA.md)), or keep it on
`127.0.0.1` / a trusted LAN only. Don't expose the bare port
to the internet.

## Transparency: privacy, network, and data

- **No telemetry, no analytics, no accounts.** The app never
  phones home. All scanning and analysis happens locally.
- **Network calls happen only when you ask for them:**
  contacting plex.tv during the optional Plex sign-in, talking
  to your own Plex server after you connect it, calling the
  OpenSubtitles API when you've configured it and use the
  wizard's search/download, posting to the webhook URL *you*
  configured when a scan finishes, and — if you accept the
  installer's offer — downloading a static ffmpeg build.
- **Your data stays in one folder** (`~/.local/share/plex-file-manager`
  on Linux, `%LOCALAPPDATA%\PlexFileManager` on Windows):
  settings, scan cache, exclusions, organize undo manifests,
  and the delete audit log. It survives updates and is never
  touched by the installer except a timestamped backup of
  `config.json` during an update.
- **Plex tokens:** the connected server's token is stored in
  `plex.json` in the data folder (permissions 0600 on Linux) —
  treat it like a password. Your plex.tv *account* token is
  kept in memory only, unless you explicitly tick "Remember my
  sign-in", in which case it is stored in that same file. No
  token is ever returned by any API response.
- **OpenSubtitles credentials:** if you configure them, your
  API key and account login live in `opensubtitles.json` in
  the data folder (permissions 0600), on the same pattern.
  Every API surface returns *presence only* (configured /
  logged-in) — never the key, password, or session token; a
  dedicated test plants secrets and sweeps the settings,
  diagnostics, state, config, and report outputs for leaks.
- **This repository is the complete source** of the shipped
  1.1.1 binaries. One disclosed divergence, in a test file
  only: the fixture lookup helper additionally accepts this
  repository's fixtures layout, and the fixture generator
  script ships in `fixtures/` — no product code or test
  assertion differs from the development tree. The public
  edition was produced from a
  private codebase by a scripted scrub gate: a denylist
  (personal names, usernames, hostnames, domains, private
  connection details) is grepped over the whole tree *and* the
  binary string tables, and the release only ships at zero
  hits, verified again on the extracted release ZIP.

## How it works (briefly)

- The scanner walks your roots, parses filenames into
  identities (title + year for movies; series + season/episode
  for TV, with common release tags stripped), and probes each
  file once with ffprobe; probe results are cached, so later
  scans are fast, and the cache is saved as a scan runs.
- Duplicate detection compares parsed identities (with
  duration as a confirmation signal and exact-hash as a
  sub-type), not file bytes alone — that's why different
  encodes of the same episode group together.
- Subtitle language detection reads the subtitle text itself
  (stopword scoring for Latin-script languages, script-based
  defaults for others — e.g. Cyrillic defaults to Russian;
  that is a default, not a detection) and compares it with
  the filename/stream language tag.
- The web UI is a single HTML file embedded in the binary;
  the plumbing names (binary, service, data folder, base URL)
  use the project's original `plex-file-manager` naming and
  stay stable so existing installs update in place.
  WellKeptLib is the product name.

## Building from source

Stdlib-only Go — no third-party dependencies. With Go 1.23+
installed:

```sh
go build -trimpath -o plex-file-manager .
bash fixtures/generate.sh   # synthetic test library (needs ffmpeg)
go test ./...
```

The release binaries are built exactly like this
(`CGO_ENABLED=0`, `-trimpath`):

```sh
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -o plex-file-manager .
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -trimpath -o plex-file-manager-arm64 .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o plex-file-manager.exe .
```

## Verification (what was actually checked for 1.1.1)

- Go test suite: **251 top-level tests, all passing**,
  counted as `--- PASS` lines from `go test -v ./...` and
  re-run on this published tree (251 PASS, 0 FAIL);
  `go vet` and `gofmt` clean; race detector clean over the
  new lock tests. (Counting note: the true pre-existing base
  is 246 — an earlier 245 figure in the 1.1.0 record was a
  counting artifact of a concurrent run, since corrected in
  the development record; the suite was green either way.)
- Shipped-binary smoke: **48/48** on the base build,
  including an **incident replay, 7/7**: a boot killed
  without cleanup leaves its lock behind, and the next start
  takes the data folder over automatically instead of
  refusing — the exact failure 1.1.0 shipped on Windows,
  replayed against the shared takeover logic on Linux. The
  public package's extracted binary passes the same smoke.
- Page checks and render harness are unchanged from 1.1.0
  (156 looked up / 179 defined; **100/100**), as is the rest
  of the product: no UI or behavior change beyond the fix.
- Installer walk on this public package: **12/12**. Honest
  limit: installer walks run the real binaries and the real
  installer logic, but `systemctl` itself is shimmed — no
  real systemd ran in the sandbox.
- Scrub gate over the extracted release ZIP (tree + binary
  strings): **zero hits** on the full denylist (personal
  names, usernames, hostnames, domains, private connection
  details, and cross-references to other private projects).
- ARM64: the binary is built and file-type verified, but it
  has **not** been run on real ARM hardware by us — reports
  welcome.

## Known limitations

- No built-in authentication (see the security warning).
- **The 1.1.1 Windows lock fix is not yet live-proven on
  Windows.** The bug it fixes was confirmed live (a real
  lockout, cleared by hand-deleting the lock file), and the
  fix's takeover logic is proven by regression tests and an
  incident replay on Linux, where the logic is shared — but
  the Windows liveness check and console-close handler
  themselves are compile-verified and logic-reviewed only;
  they cannot execute in the build environment. The first
  Windows start after an unclean close is the live proof.
- **The hardlink library mode is not yet live-proven.**
  Real cross-device (EXDEV) failures and Windows hardlink
  execution are verified at an injected-function test seam
  — they cannot execute in the build environment — and the
  mode has not yet had a live run on real hardware (the
  maintainer's own slot/Windows test is pending). The Linux
  same-filesystem path is fully test- and smoke-proven,
  including the source-untouched invariant. Hardlinks also
  inherently require source and destination on the same
  filesystem, and a destination inside your scan roots makes
  linked copies appear as duplicates (the UI warns).
- **OpenSubtitles is not yet live-verified.** No real API key
  was available in the build environment, so the client is
  verified against a mock implementing the documented
  request/response shapes. A few live response shapes aren't
  pinned byte-for-byte (the search list key, exact quota field
  names, download-link header expectations); the parser is
  deliberately tolerant, and quota figures are read from the
  API's own responses, never hardcoded. If you try it with a
  real key, issue reports are very welcome.
- Scheduled scans run inside the app process — the app must
  be running for a schedule to fire. Missed slots are not
  queued; the next slot is consumed once when the app next
  runs.
- Image-based subtitle tracks (PGS/VobSub) can't be
  language-verified.
- Subtitle language detection is heuristic: short files and
  languages sharing a script can be misjudged; findings show
  their evidence so you can decide.
- A first scan of a very large library takes a while (every
  file is probed once). Raise **Probe workers** in Settings
  if it's slow; later scans reuse the cache.

## Release notes — 1.1.1 (hotfix; supersedes 1.1.0)

- **Windows startup lockout fixed.** In 1.0.0–1.1.0, the
  single-instance lock in the data folder could never be
  recognized as stale on Windows, and closing the app window
  never released it — so after the first close, every later
  start exited immediately claiming another copy was running.
  1.1.1 checks process liveness for real, releases the lock
  when the window closes, and treats a lock left by a dead
  process as stale. **If 1.1.0 locked you out, just start
  1.1.1** — it clears the dead lock itself; nothing needs
  deleting by hand (deleting
  `%LOCALAPPDATA%\PlexFileManager\app.lock` also works).
- The 1.1.0 public release has been **withdrawn** — please
  use this build instead. Nothing else changes: no new
  features, no UI changes, no behavior changes beyond the
  fix and the version number.

## Release notes — 1.1.0

- **Organize gains a hardlink mode:** build the Jellyfin
  layout as hardlinks under a separate destination folder;
  originals are never touched (proven by full-tree snapshots
  in tests and smoke). Preview classifies every file
  (create / already-linked / collision with keeper ranking /
  unresolved / not-identified / unlinkable / target-conflict),
  Apply is manifest-first and incremental, Undo removes only
  links still pointing at their recorded source.
- Symlink fallback was considered and rejected: links would
  rot when sources move, and several players resolve
  symlinks inconsistently. Hardlinks keep every path real.
- No other changes: scanner, duplicates, subtitles, reports,
  scheduler, webhook, and CLI behavior are identical to
  1.0.0, and every pre-existing test passes unmodified.

## Release notes — 1.0.0 (changes since 0.7.0)

- **Hardlink truth in Duplicates:** reclaimable vs apparent
  space, hardlink copies flagged, estimates labeled.
- **Report tab** with sectioned CSV and self-contained HTML
  export, numbers guaranteed equal to the app's own API.
- **Scan history** (last 50) with per-scan deltas and
  missing-root-safe comparisons.
- **Scheduled scans** (interval or daily, in-process) and a
  **scan-finished webhook** (generic / Discord / ntfy,
  counts-only payload).
- **CLI mode:** `-scan-once` with documented exit codes and a
  data-folder lock shared with the server (a second instance
  refuses cleanly instead of corrupting state).
- **Subtitle wizard, complete:** overview, fingerprint-bound
  dismissals, relabel with undo, guarded delete, coverage and
  gap views, and OpenSubtitles search/download with your own
  API key (mock-verified; see limitations).
- **Missing episodes, subtitle coverage, folder hygiene**
  (orphan sidecars, empty folders).
- **Embedded-subtitle audit toggle** in Settings (it can
  finally be switched off; off means off — no extraction, no
  embedded findings).
- 0.7.0 brought: `install.sh` (install / update-with-rollback
  / uninstall on any Linux server), the ARM64 build, and the
  WellKeptLib name (display surfaces only; plumbing names
  unchanged, existing installs update in place).

## License

MIT — see [LICENSE](LICENSE). Copyright (c) 2026
WellKeptLib contributors.
