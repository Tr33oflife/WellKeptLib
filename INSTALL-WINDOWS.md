# Install — Windows (WellKeptLib v0.7.0)

The Windows shape is the secondary one (the seedbox/Ultra shape is
canonical), and it is simple: one .exe, no installer, no runtime.

## Click-by-click

1. Extract `WellKeptLib-0.7.0-public.zip` to a folder you keep
   (e.g. `Documents\WellKeptLib`). You will see
   `plex-file-manager.exe` (Windows) and `plex-file-manager` (Linux —
   ignore that one on Windows).
2. Double-click **`plex-file-manager.exe`**. A console window opens
   and stays open — that window *is* the app running. Leave it open
   while you use the app; close it to stop the app.
3. Windows SmartScreen may warn because the exe is unsigned:
   click **More info → Run anyway**.
4. Your browser opens automatically at
   **http://127.0.0.1:8787/**. If it doesn't, open that address
   manually in any browser.
5. First run:
   - In the **Library roots** box at the top, type or paste one
     folder per line (e.g. `D:\Movies`, `E:\TV Shows`). Pasting with
     Windows "Copy as path" is fine — the surrounding quotes are
     stripped automatically. If a folder can't be found, the app
     now tells you by name instead of scanning nothing.
   - Click **▶ Start scan**. Progress shows in the header; a first
     scan of a big library takes a while because every file is
     probed once (later scans reuse the cache).
   - Browse the results in the **Library**, **Subtitles**,
     **Duplicates**, and **Health** tabs.
6. If the header says **ffprobe NOT FOUND**: the app needs ffmpeg's
   ffprobe/ffmpeg. Three ways to fix it: install ffmpeg so it's on
   your PATH; **or simply drop `ffprobe.exe` and `ffmpeg.exe` in the
   same folder as `plex-file-manager.exe`** (or a `bin` folder next
   to it) and restart the app — they're picked up automatically; or
   open the **Settings** tab and paste the full paths there, then
   Save.
7. Your settings, scan cache, and exclusions are stored in
   `%LOCALAPPDATA%\PlexFileManager` — uninstalling is just deleting
   the folder and that data folder. Scanning is read-only: a scan
   never renames, moves, or deletes anything. The one exception is
   the **Organize** tab below, which moves files only when you
   preview a plan and press Apply — and every organize can be
   undone. Nothing in the app ever deletes a media file.

## Organize into Jellyfin folders (new in v0.3.0)

The **Organize** tab turns a messy folder (a downloads folder, a
fresh rip dump) into Jellyfin's layout:

- Movies → `Title (Year)\Title (Year).ext`
- TV → `Series\Season 01\Series - S01E01 - Episode Title.ext`
  (Season 00 for specials; multi-episode files become
  `S01E01-E02`)
- Subtitles that share a video's name, `.nfo` files, and folder
  artwork (poster.jpg and friends) travel with their video.

How to use it:

1. Open the **Organize** tab, type or pick the folder, and click
   **Preview plan**. The preview lists every move it would make,
   plus anything it *won't* touch: files it couldn't identify (no
   year for a movie, no episode number for a show — those stay
   exactly where they are) and name collisions (flagged, never
   overwritten).
2. Review the plan, then click **Apply N moves**. Before anything
   moves, the app saves an undo record (a "manifest") into its data
   folder (`%LOCALAPPDATA%\PlexFileManager\organize-manifests`) and
   checks once more that the folder still matches the preview — if
   something changed, it refuses and moves nothing.
3. Made a mistake? **Undo this organize** (right there, or later
   from **Past organizes** on the same tab — manifests survive
   restarts) restores the exact original layout.

Organize only ever works inside the one folder you pick, moves are
renames within that folder (no cross-drive moves), and folders left
empty by a move are reported, never deleted.

## Optional: connect Plex to import library paths

Every file tool in this app works without Plex — this only saves
typing paths and adds one cross-check.

**Sign in (the normal way):**

1. Open the **Settings** tab, "Plex (optional)" card, and click
   **Sign in with Plex**.
2. The app shows an **Approve on Plex** button. Click it: Plex's
   own page opens in your browser — you're already signed in to
   Plex there — so approve the sign-in and come back to the app.
   The app notices the approval on its own. There is no code to
   type anywhere.
3. Pick your server from the list the app shows and click
   **Connect**. The card now reads **Connected ✓ — your server's
   name**, and the connected server's token is stored in
   `plex.json` in the data folder (`%LOCALAPPDATA%\PlexFileManager`)
   — treat that file like a password. Your plex.tv account token
   is kept only in the app's memory while it runs — UNLESS you
   tick **Remember my sign-in** in the server picker, in which
   case it is stored in that same `plex.json` so server
   switching (below) also works after a restart. Either way, no
   token is ever returned by any API response.
4. Back at the top of the page, click **⇓ Import roots from Plex**,
   tick the library folders you want, and **Use selected roots** —
   they fill the Library roots box. Press **▶ Start scan** when
   ready; importing alone never starts a scan.
5. On the **Health** tab, **Check for Plex items missing on disk**
   lists anything Plex knows about whose file isn't where Plex
   thinks it is.

**Switching servers (new in v0.3.5):** you don't have to
disconnect and sign in again to change servers. While the app
is running, the connected card shows **Change server…** —
click it, pick a different server from the fresh list, and the
app connects to it. If you ticked **Remember my sign-in** when
you connected (step 3), switching also works after restarting
the app; **Forget saved sign-in** (shown in the picker when a
sign-in is remembered) clears just the remembered sign-in and
leaves the current server connected.

**Manual connection (fallback, for unclaimed or local-only servers
that can't use the plex.tv sign-in):** open **Advanced: manual
connection (URL + token)** in the same Settings card.

1. In Plex's web UI, get your token: open any library item →
   **⋯ → Get Info → View XML** — the URL ends with
   `X-Plex-Token=<redacted> (Plex's support article "Finding an
   authentication token" describes the same route.)
2. Paste the server URL and the token. The address may be typed
   with or without `http://` — `192.168.1.50:32400` is fine, the
   scheme is added when missing.
3. **Test connection** checks what's currently typed in the form
   (you don't have to save first), and **Save Plex settings** runs
   the same test right after saving, so you know immediately
   whether it worked.

**Different machines?** If Plex runs on another machine (e.g. Plex
on your seedbox, this app on Windows), the paths Plex reports
won't match this machine's. Add **path mappings** in the same
Settings card — server path → this machine's path (e.g.
`/home/you/media` → `D:\Media`) — and both the import and the
cross-check will translate through them, however you connected.

**Slow first scan?** A first scan of a big library probes every
file once. If it feels slow, raise **Probe workers** in Settings
(the default is 4). Later scans reuse the probe cache — and since
v0.3.1 the cache is saved as the scan goes, so even a scan you
cancel keeps the files it already probed. New in v0.3.4: on a
first scan of a large library, the library results appear right
after probing finishes — the embedded-subtitle audit keeps
running afterwards (the header shows it) and folds its findings
in when done, and the audit is cached per file, so later scans
skip it for unchanged files.

## Health tab actions (new in v0.3.5)

Every finding on the **Health** tab now has two actions:

- **Open location** opens Explorer at the flagged file, with
  the file selected, so you can inspect or handle it yourself.
  *(Fixed in v0.3.6: in v0.3.5 this could open Explorer's
  default view instead whenever the file's path contained
  spaces — which is most library paths. It now lands on the
  file.)*
- **Delete file** deletes that file — permanently. It is
  deliberately narrow: the app will only delete a file that is
  a *current* Health finding (exactly the list on screen),
  sitting under one of your scan roots, and never a folder.
  Every deletion is recorded in `deleted-actions.json` in the
  data folder (when, what path, how big, and which finding
  condemned it), so there is always an audit trail of what
  went.

## Duplicates tab actions (new in v0.3.6)

Every copy in a duplicate group on the **Duplicates** tab now
has the same two actions:

- **Open location** opens Explorer at the copy, with the file
  selected.
- **Delete** deletes that one copy — permanently. The guards
  mirror the Health delete and add one of their own: the app
  will only delete a file that is a copy in a *current*
  duplicate group (exactly the groups on screen), sitting
  under one of your scan roots — and **never the group's last
  remaining copy**. Deleting copies from a two-copy group
  leaves the survivor in place and simply dissolves the
  group; there is no way to delete every copy of something
  from this tab. The confirm panel shows how many copies will
  remain, and warns you when the copy you're deleting is the
  suggested keeper (★). Every deletion is recorded in the same
  `deleted-actions.json` audit log, with the group it came
  from.

**Rescan (new in v0.3.7):** the tab also has a **↻ Rescan**
button — it runs the same scan as the header's Start scan
(same roots, same everything) without leaving the tab, shows
the live progress while it runs, and the groups refresh
themselves when the new results land. And since v0.3.7,
**Delete** also handles read-only files: if Windows refuses
the delete because the file is read-only, the app makes it
writable and retries once (the same applies to the Health
tab's delete).

## Optional: command-line flags

From a terminal in the extracted folder:

```
plex-file-manager.exe -addr 127.0.0.1:8788 -no-browser
```

- `-addr host:port` — listen address (default `127.0.0.1:8787`).
  Change the port if something else already uses 8787.
- `-data <folder>` — where config/cache/results live
  (default `%LOCALAPPDATA%\PlexFileManager`).
- `-baseurl /path` — only needed behind a reverse proxy (Ultra).
- `-no-browser` — don't auto-open the browser.
