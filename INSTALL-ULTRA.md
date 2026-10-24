# Install — Ultra.cc (WellKeptLib v1.3.3)

## Easy way: run ./install.sh

The release package ships an installer that does everything on
this page for you — on Ultra and on any other Linux server:

1. Extract the release ZIP on the server.
2. `cd` into the extracted folder and run `./install.sh`
3. Answer two questions (port, and proxy vs direct-LAN mode). On
   Ultra, use a port from your assigned range.

The same script also UPDATES an existing install (it detects the
old one, backs up the binary and config, swaps, verifies the new
version answers, and rolls back by itself if it doesn't), and
`./install.sh --uninstall` removes it (your data folder is kept
unless you explicitly say otherwise). The manual steps below are
the reference for exactly what the script does — and the fallback
if you would rather do it by hand.

This is the canonical install shape: a headless server + web GUI.
Everything below follows Ultra's generic software installation guide:
no sudo, no Docker, nothing outside your home directory, and only a
port inside your assigned range.

**Placeholders — replace before running:**
- `PORT` = a free 5-digit port from *your* assigned range
  (UCP → your service details). Example below uses `PORT=XXXXX`
  markers — substitute your real port everywhere it appears.
- The base URL path is `/plex-file-manager`. Your final address will
  be `https://<your-slot-hostname>/plex-file-manager` — the
  hostname shown in your Ultra service details (UCP).

## Steps

1. **SSH into your slot** (per Ultra's SSH guide), then check the
   media tools the app needs:
   ```
   which ffprobe ffmpeg
   ```
   If both print paths, skip to step 2. If they don't, install a
   static ffmpeg build into your home directory (no sudo needed):
   ```
   mkdir -p ~/bin ~/ffmpeg-static && cd ~/ffmpeg-static
   wget https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-amd64-static.tar.xz
   tar xf ffmpeg-release-amd64-static.tar.xz
   cp ffmpeg-*-amd64-static/ffmpeg ffmpeg-*-amd64-static/ffprobe ~/bin/
   cd ~
   ```
   …and after the app is running, paste the FULL paths —
   `/home/<user>/bin/ffprobe` and `/home/<user>/bin/ffmpeg`
   (with your slot username in place of `<user>`) — into its
   **Settings** tab. Settings does not expand `~`, so the
   `~/bin/…` shorthand will not work there; the full path is
   required.

2. **Put the binary in place.** Transfer the Linux binary from the
   release package — the file named `plex-file-manager` (no
   extension) — to your slot's home directory, e.g. with scp from
   your Windows machine:
   ```
   scp plex-file-manager <user>@<your-slot-hostname>:~/bin/plex-file-manager
   ```
   (or use your usual SFTP client / the UCP file manager). Then, on
   the slot:
   ```
   mkdir -p ~/bin ~/.local/share/plex-file-manager
   chmod +x ~/bin/plex-file-manager
   ```

3. **Smoke-test it in the foreground** (pick your PORT):
   ```
   ~/bin/plex-file-manager -addr 127.0.0.1:PORT -data ~/.local/share/plex-file-manager -baseurl /plex-file-manager -no-browser
   ```
   It should print `Listening on http://127.0.0.1:PORT/plex-file-manager/`.
   Press `Ctrl+C` to stop it — the systemd service runs it properly.

4. **Create the user systemd service:**
   ```
   nano ~/.config/systemd/user/plex-file-manager.service
   ```
   Paste (with your real PORT substituted):
   ```ini
   [Unit]
   Description=WellKeptLib
   After=network-online.target

   [Service]
   Type=exec
   Restart=on-failure
   ExecStart=%h/bin/plex-file-manager -addr 127.0.0.1:PORT -data %h/.local/share/plex-file-manager -baseurl /plex-file-manager -no-browser
   StandardOutput=file:%h/.local/share/plex-file-manager/server.log
   StandardError=file:%h/.local/share/plex-file-manager/server.log

   [Install]
   WantedBy=default.target
   ```
   Save (`Ctrl+X`, `Y`, `Enter`), then:
   ```
   systemctl --user daemon-reload
   systemctl --user enable --now plex-file-manager.service
   systemctl --user status plex-file-manager.service
   ```
   Status should show `Active: active (running)`.

5. **Expose it through nginx (HTTPS):**
   ```
   nano ~/.apps/nginx/proxy.d/plex-file-manager.conf
   ```
   Paste (with your real PORT substituted):
   ```nginx
   location /plex-file-manager/ {
       proxy_pass              http://127.0.0.1:PORT;
       proxy_http_version      1.1;
       proxy_set_header        X-Forwarded-Host        $http_host;
       proxy_set_header        X-Forwarded-Proto       $scheme;
   }
   ```
   Save, then restart the web server:
   ```
   app-nginx restart
   ```

6. **Open it:** `https://<your-slot-hostname>/plex-file-manager`
   First run: type your library folders into **Library roots** (one
   per line, e.g. `~/media/Movies`), click **▶ Start scan**, and
   browse the tabs. Settings, cache, and exclusions live in
   `~/.local/share/plex-file-manager`, so they survive restarts and
   binary upgrades.

Day-to-day: `systemctl --user restart plex-file-manager.service` to
restart; replace `~/bin/plex-file-manager` with the new binary from a
future release (after stopping, or restart right after) to upgrade.

## Organize into Jellyfin folders (new in v0.3.0)

The **Organize** tab turns a messy folder into Jellyfin's layout —
movies as `Title (Year)/Title (Year).ext`, TV as
`Series/Season 01/Series - S01E01 - Episode Title.ext` (Season 00
for specials, `S01E01-E02` for multi-episode files) — with
same-name subtitles, `.nfo` files, and folder artwork travelling
with their video. It is the app's ONLY file-moving feature, and it
is guarded:

1. Pick a folder in the **Organize** tab and click **Preview plan**.
   The preview lists every planned move, the files it couldn't
   identify (left exactly where they are — no year for a movie, no
   episode number for a show), and any name collisions (flagged,
   never overwritten).
2. **Apply** re-checks the folder against the preview first (if the
   tree changed, it refuses and moves nothing), writes an undo
   manifest to `~/.local/share/plex-file-manager/organize-manifests/`
   BEFORE the first file moves, and updates it after every file.
3. **Undo** — from the tab, immediately or weeks later (manifests
   survive restarts) — restores the exact original layout.

Moves are renames within the one folder you pick (same filesystem,
no cross-device copies) and empty folders left behind are reported,
never deleted. Scanning stays fully read-only.

## Optional: connect Plex to import library paths

Every file tool in this app works without Plex — this only saves
typing paths and adds one cross-check.

**Sign in (the normal way):**

1. In this app's **Settings** tab, "Plex (optional)" card, click
   **Sign in with Plex**.
2. The app shows an **Approve on Plex** button. Click it: Plex's
   own page opens in your browser — you're already signed in to
   Plex there — so approve the sign-in and come back to the app.
   The app notices the approval on its own. There is no code to
   type anywhere.
3. Pick your server from the list and click **Connect**. The card
   now reads **Connected ✓ — your server's name**.
4. Click **⇓ Import roots from Plex** at the top of the page, tick
   the library folders, and **Use selected roots**; press
   **▶ Start scan** when ready (importing alone never scans).
5. On the **Health** tab, **Check for Plex items missing on disk**
   lists anything Plex knows about whose file isn't on disk.

**Manual connection (fallback, for unclaimed or local-only servers
that can't use the plex.tv sign-in):** open **Advanced: manual
connection (URL + token)** in the same Settings card. Get the token
from Plex's web UI (any library item → **⋯ → Get Info → View
XML** — the URL ends with `X-Plex-Token=<redacted> and paste the
server URL (on the same slot that's usually
`http://127.0.0.1:32400`) and the token. The address may be typed
with or without `http://` — it's added when missing. **Test
connection** checks what's currently typed (no save needed first),
and **Save Plex settings** runs the test right after saving.

**Path mappings:** if Plex and this app ever see the files at
different paths, add server path → local path rows in the same
Settings card; the import and cross-check translate through them,
however you connected.

**Token storage on Ultra:** the connected server's token lives in
`plex.json` in the data dir
(`~/.local/share/plex-file-manager/plex.json`, permissions 0600) —
treat it like a password. Your plex.tv *account* token is kept
only in the app's memory while it runs — UNLESS you tick
**Remember my sign-in** in the server picker, in which case it is
stored in that same `plex.json` (still 0600) so server switching
also works after a restart. No token is ever returned by any API
response. Remember the app URL still has no login of its own (see
the security note below): anyone with the link can *use* the app
through its web UI.

**Switching servers (new in v0.3.5):** no disconnect / sign-in
round trip needed — the connected card's **Change server…**
button fetches your account's server list fresh and switches.
It works any time the app is running; with **Remember my
sign-in** ticked it also works after a restart. **Forget saved
sign-in** in the picker clears just the remembered sign-in.

**Health tab actions (new in v0.3.5):** each Health finding has
**Open location** and **Delete file**. On Ultra there is no file
manager to open, so Open location instead shows the finding's
full path with a copy button. Delete is guarded the same way
everywhere: current findings only, under a configured scan
root, regular files only (never folders), and every deletion
is logged to `deleted-actions.json` in the data dir.

**Duplicates tab actions (new in v0.3.6):** each copy in a
duplicate group has the same **Open location** (on Ultra: the
copy's full path with a copy button) and **Delete**. The
delete only accepts a copy in a *current* duplicate group,
under a configured scan root, and **never the group's last
remaining copy** — deleting from a two-copy group dissolves
the group and leaves the survivor untouched. Deletions are
logged to the same `deleted-actions.json`, with the group
they came from. (v0.3.6 also fixes the Windows Open location
— Explorer now opens at the file itself; on the headless slot
the behaviour is unchanged.) **New in v0.3.7:** the tab has a
**↻ Rescan** button that runs the same scan as the header's
Start scan (same roots, same everything) without leaving the
tab; it shows the live progress while running and the groups
refresh themselves when the new results land.

**Slow first scan?** A first scan of a big library probes every
file once. If it feels slow, raise **Probe workers** in Settings
(the default is 4). Later scans reuse the probe cache — and since
v0.3.1 the cache is saved as the scan goes, so even a scan you
cancel keeps the files it already probed. New in v0.3.4: on a
first scan of a large library, the library results appear right
after probing finishes — the embedded-subtitle audit keeps
running afterwards (the header shows it) and folds its findings
in when done, and the audit is cached per file, so later scans
skip it for unchanged files. **New in v0.8.0:** if you don't need
the embedded check at all, turn it off — **Settings → Embedded
subtitle audit**. With it off, scans finish at the probe results:
no embedded tracks are extracted or checked, the Subtitles tab
shows external subtitle files only, and it says so at the top of
the tab. Turn it back on and the next scan re-audits, reusing the
per-file cache for anything unchanged.

**New in v0.9.0:** the Subtitles tab opens with a count of every
problem class and a **Fix step by step** wizard: mislabeled
external subtitle files can be relabeled to the detected
language (preview → apply → undo, exactly like Organize),
findings you have checked can be marked verified so they stop
flagging (they resurface if the file changes), and a coverage
list shows which items lack subtitles in your wanted languages
(**Settings → Wanted languages**, default `eng`). The Library
tab lists episodes missing between the first and last present
in each season, and Health now also flags orphan sidecar files
(subtitles/.nfo/artwork whose video is gone). Embedded tracks
are never renamed or deleted — their path is the video file
itself, and the app does not rewrite media.

**New in v1.0.0:** the milestone release. **Duplicates are now
hardlink-aware**: groups show file entries vs physical copies,
copies hardlinked to the keeper are marked, and the
**reclaimable** figure counts only what deleting down to one
physical copy would actually free (the old figure remains as
*apparent*). A **Report** tab totals the whole library from the
same numbers the tabs show, with CSV + standalone HTML
downloads; a **History** tab keeps the last 50 scans with
per-scan deltas. Under **Settings → Automation**: **scheduled
scans** (every N hours or daily at a set time — in-process, so
the app must be running; a due scan that finds the app busy is
skipped and recorded, never queued) and a **scan-finished
webhook** (generic JSON / Discord / ntfy; counts only, never
paths or credentials; fire-and-forget — a dead endpoint can't
slow a scan). The binary also gains a headless mode:
`plex-file-manager -scan-once [-report out.json]
[-fail-on-findings]` runs one scan and exits (0 done · 1 scan
failed · 2 config error or data dir locked · 3 findings with
`-fail-on-findings`). Only one process may own a data dir now —
a second instance refuses with a clear message instead of
interleaving writes. Finally, the subtitle wizard can
**download missing subtitles from OpenSubtitles** (Step F):
bring your own API key in **Settings → OpenSubtitles**
(downloads also need your account login; they count against
your quota, and the wizard shows the remaining number the API
itself reports). Downloads are verified with the app's own
language detector and never overwrite an existing file.

**New in v1.3.3:** a file the app measures as 0 bytes
still gets its hardlink. When a link build is applied, the
app re-checks each file against the preview; a size
recorded as 0 at preview time used to count as "changed"
if the file measured at its real size later, and that one
entry's link was skipped. A recorded 0 now means "size
unknown" — the link is made, while the app still verifies
it is the same file (same identity and modification time)
before linking. Files with a real recorded size keep the
exact check.

**New in v1.3.2:** a Ko-fi Donate button that appears when
the app is online and silently stays away when it isn't.
Settings has a Ko-fi username (prefilled) and a "Show Ko-fi
Donate button" toggle (on by default). Turn the toggle off
and the app never even tries to load the button; leave the
username blank and the button is off entirely. Everything
else works exactly as before with no connection.

**New in v1.3.1:** two fixes from the first real
link-problems export. Link-plan keeper picks now always use
real file sizes (unprobed copies used to rank on "0 bytes"
and fall to path order), and episodes whose titles start
with numbers (like "500 Keys") are no longer mistaken for
fractional episodes. Genuine half-episodes still work as
before.

**New in v1.3.0:** the link plan can now export its
problems. After previewing a link plan in the Organize tab's
**Build link library** mode, the new **Export problems
(JSON)** button (next to the Build button) downloads one
JSON file listing everything that could not be linked -
every collision with the keeper decision and the evidence
behind it, every file the app could not identify with the
reason and what it did manage to read from the name, and
every unlinkable file with the filesystem facts - so naming
problems can be diagnosed in bulk instead of one row at a
time. Nothing else changes.

**New in v1.2.0:** pick your colors. A theme selector in
the page header (and a swatch picker under Settings -
Appearance) switches between six themes - Midnight (the
original look, still the default), Daylight (light), Ocean,
Ember, Forest and Plum. The choice applies instantly and is
saved on the server, so it follows you across browsers and
machines. The app also has its own icon now - a book with a
check on an amber tile - in the browser tab and beside the
title.

**New in v1.1.1:** a Windows-only startup fix — on Windows,
after closing the app window, the app could refuse to start
again, claiming another copy was running, even though
nothing was running. A leftover lock file from a process
that is no longer running is now recognized and cleared
automatically. Nothing changes on Ultra/Linux beyond the
version number.

**New in v1.1.0:** the Organize tab gains a second mode —
**Build Jellyfin library (hardlinks)**. Point it at a source
folder (your downloads stay exactly where they are) and a
separate target folder; the preview plans the same Jellyfin
layout Move mode would produce, and Apply creates
**hardlinks** in the target instead of moving anything.
Originals are never modified, renamed, or deleted — seeding
is unaffected, and the linked library costs no extra disk
space (a hardlink is another name for the same file). The
preview proves per file, before anything is created, that
source and target share a filesystem — hardlinks cannot
cross filesystems or volumes, and anything that can't be
proven is listed as *unlinkable* with the reason. When
several copies want one name, the preview shows which copy
wins and why (resolution → HDR → bitrate → size), lists the
alternates, and refuses to pick when the copies' durations
say they may be different cuts. Every build writes an undo
record first (`link-manifests/` in the data folder); Undo
removes only the links that build created and verified — a
target that was replaced since is left untouched and
reported. **Setup guidance:** keep the target folder
OUTSIDE your scan roots; if it sits inside one, the preview
warns you — a scan would list the linked library alongside
the originals (the hardlink-aware counts keep the numbers
honest, but the library view gets noisy). Two honest
caveats: editing a file through either name edits the
shared content, because they are the same file; and
deleting the original does not free disk space while a
link to it remains.

## Security note (optional but read it)

This app has **no login of its own** — the same caveat Maintainerr's
docs carry. On Ultra that means the proxied URL above is reachable by
**anyone who has the link**, and it shows your file paths and library
inventory. Since v0.3.0 it matters more: the link also reaches the
**Organize** tab, which can move files inside any folder the app can
read on the slot — and since v0.3.5 the Health tab's guarded
**Delete file**, and since v0.3.6 the Duplicates tab's guarded
**Delete** (current findings / group copies under your scan
roots only, never a group's last copy, logged). If you share the URL or just want a password on
it, add nginx basic auth:

1. Create a password file in your home directory:
   ```
   printf "user:%s\n" "$(openssl passwd -apr1)" > ~/.apps/nginx/plex-file-manager.htpasswd
   ```
   (`openssl passwd -apr1` prompts you to type the password; it is
   stored only as a hash. Change `user` to any username you like.)
2. Add two lines inside the `location /plex-file-manager/` block in
   `~/.apps/nginx/proxy.d/plex-file-manager.conf`:
   ```nginx
   auth_basic           "WellKeptLib";
   auth_basic_user_file /home/<user>/.apps/nginx/plex-file-manager.htpasswd;
   ```
   (replace `<user>` with your slot username — the file path in
   nginx config must be absolute.)
3. `app-nginx restart`, then reload the URL — your browser will ask
   for the username and password.

Recommended if the URL is shared with anyone; skippable if it stays
private to you.
