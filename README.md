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

- **Version:** 0.7.0
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
  group's last copy, always logged).
- **Subtitle truth-checking.** Subtitle files and embedded
  tracks are checked against their *text*, not their labels: a
  track tagged English whose cues are actually Spanish is
  flagged, with the detected language, a confidence value, and
  sample lines as evidence. Image-based tracks (PGS/VobSub)
  are reported as "cannot verify", never guessed.
- **Library health.** Empty and truncated files, unparseable
  containers, missing video streams, broken subtitles, and
  leftover download artefacts (`.part`, `.crdownload`, …) —
  each finding carries the probe evidence that caused it.
  Findings have guarded per-file actions (open location,
  delete) with an audit log.
- **Inventory by size.** Movies, series, and seasons sorted
  by size, per-root totals, biggest files overall.
- **Organize into Jellyfin folders.** Pick a messy folder and
  preview a full Jellyfin-layout plan (`Title (Year)/…`,
  `Series/Season 01/Series - S01E01 - Title.ext`) before
  anything moves. Apply re-validates the tree, writes an undo
  manifest *before* the first move, and Undo restores the exact
  original layout — weeks later, if you like. Unidentified
  files are left in place and listed with reasons; collisions
  are flagged, never overwritten.

Scanning is always read-only. The only features that touch
files are Organize (preview → apply → undo) and the guarded
per-file deletes on the Health and Duplicates tabs, each of
which validates against the current findings and writes an
audit log (`deleted-actions.json` in the data folder).

## Install

Get `WellKeptLib-0.7.0-public.zip` from the
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
  to your own Plex server after you connect it, and — if you
  accept the installer's offer — downloading a static ffmpeg
  build.
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
- **This repository is the complete source** of the shipped
  0.7.0 binaries. The public edition was produced from a
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
go test ./...
```

The release binaries are built exactly like this
(`CGO_ENABLED=0`, `-trimpath`):

```sh
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -o plex-file-manager .
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -trimpath -o plex-file-manager-arm64 .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o plex-file-manager.exe .
```

## Verification (what was actually checked for 0.7.0)

- Go test suite: **165 tests, all passing**; `go vet` and
  `gofmt` clean.
- Installer: scripted end-to-end walks of fresh installs
  (proxy and LAN modes), an update from the previous version,
  a deliberately broken update (automatic rollback), and an
  uninstall — **36/36 checks** on the base build, plus a
  fresh-install walk of this public package (**12/12**).
  Honest limit: those walks run the real binaries and the
  real installer logic, but `systemctl` itself is shimmed —
  no real systemd ran in the sandbox.
- Shipped-binary smoke on a fixture library: version 0.7.0,
  scan baseline 11 media / 2 duplicate groups / 2 subtitle
  findings / 6 health findings, correct branding in the served
  page.
- Scrub gate over the extracted release ZIP (tree + binary
  strings): **zero hits** on the full denylist.
- ARM64: the binary is built and file-type verified, but it
  has **not** been run on real ARM hardware by us — reports
  welcome.

## Known limitations

- No built-in authentication (see the security warning).
- Image-based subtitle tracks (PGS/VobSub) can't be
  language-verified.
- Subtitle language detection is heuristic: short files and
  languages sharing a script can be misjudged; findings show
  their evidence so you can decide.
- A first scan of a very large library takes a while (every
  file is probed once). Raise **Probe workers** in Settings
  if it's slow; later scans reuse the cache.

## Release notes — 0.7.0

- New `install.sh`: one script installs, updates (with
  automatic rollback), and uninstalls on any Linux server;
  ARM64 build added.
- First release under the WellKeptLib name (display surfaces
  only; plumbing names unchanged, existing installs update in
  place).
- Carries forward: the Duplicates interface overhaul,
  per-file guarded actions on Health and Duplicates, Plex
  sign-in with server switching, Organize with preview /
  apply / undo, and the Android back-button fix (Back steps
  through tabs; double-back at the first tab is the only exit).

## License

MIT — see [LICENSE](LICENSE). Copyright (c) 2026
WellKeptLib contributors.
