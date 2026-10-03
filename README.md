# ppgmods

Recover, install and update People Playground C# mods after the September 2026
Workshop shutdown, without reopening the hole the worm came through.

On 21–24 September 2026 a worm spread through the People Playground Workshop by
uploading itself and inserting its code into existing mods. Valve deleted every
Workshop item containing C# code, and the game no longer runs mods. Loaders such
as [RE_PPG](https://github.com/AlibardaWasTaken/RE_PPG) (BepInEx) bring mod
loading back, but the mods themselves now have to come from somewhere else.
`ppgmods` gets them from the remaining sources and scans each one before it
reaches your `Mods` folder.

> **ppgmods does not make mods safe.** Mods are code that runs with your
> permissions. The scanner blocks the known worm techniques and anything that
> obviously doesn't belong in a sandbox mod, but obfuscated malware can get
> past any static scanner. Install mods from authors you trust.

Not affiliated with Studio Minus, Valve, GameBanana or Skymods.

## Sources

| Source | What it is | Downloads | Notes |
|---|---|---|---|
| Local Steam cache | `steamapps/workshop/content/1118200` on your PC | `backup-workshop` | **Best source, and it won't last.** Steam deletes removed items when it syncs. Back the cache up before launching Steam online. |
| [True Workshop](https://ppgworkshop.onrender.com/) | Community archive of uploaded PPG mods (~140). Uploads pass the site's scanner and most are **reviewed by its maintainers** | Automatic | Files are on a public Hugging Face dataset; each item lists its SHA-256, which ppgmods checks. Reviewed items show "✓ reviewed" and skip the cooldown; unreviewed ones are marked "not reviewed" and wait out the cooldown. Updates are detected when an item's file changes. |
| [GameBanana](https://gamebanana.com/games/7715) | Live mod site, ~560 PPG submissions, authors still uploading | Automatic | Public API, MD5 checksums and server-side antivirus results. This is the only source `update` follows. |
| [Skymods](https://catalogue.smods.ru/game/people-playground/) (smods.ru) | Third-party mirror of Steam Workshop items, ~9,000 PPG entries mirrored up to 20–21 Sep 2026. **The biggest surviving copy.** | Automatic | Files live on modsbase.com. ppgmods waits out the page's countdown, presses "Create download link" the way the site's own button does, and downloads the file (about 10 s per mod). If modsbase shows a Cloudflare check or a captcha, ppgmods does not try to get past it: it opens the page in your browser and imports the file when it lands in Downloads. Copies revised on or after 21 Sep 2026 are refused. Beware look-alike sites: the real one is **smods.ru**, not "skymods.it.com". |
| [top-mods](https://top-mods.com/mods/people-playground) | Second Workshop mirror, ~11,000 PPG entries. It often has a **newer revision** than Skymods, mods Skymods never copied, and its own copies of the preview images | Automatic | Files are on modsfire.com, with a modsbase.com alternate link. Both are downloaded the way their own buttons do it. |

## Install

1. Install a loader that runs C# mods again, such as
   [RE_PPG](https://github.com/AlibardaWasTaken/RE_PPG/releases).
2. Download the latest release from
   [Releases](https://github.com/DogeKingC/SWG/releases/latest):
   - **Windows:** `ppgmods-windows-amd64.exe`. Double-click it. SmartScreen
     may warn because the program isn't code-signed; choose *More info → Run
     anyway*.
   - **Linux:** `ppgmods-linux-amd64`. Run
     `chmod +x ppgmods-linux-amd64 && ./ppgmods-linux-amd64`.
3. The window opens with an **Install** banner. Click it to install
   PPG Mod Manager as a normal app (no admin rights needed):
   - **Windows:** `%LocalAppData%\Programs\PPG Mod Manager`, with Start menu
     and desktop shortcuts and an entry in *Apps & features* for uninstalling.
   - **Linux:** `~/.local/bin/ppgmods`, with an application-menu entry, icon
     and desktop shortcut.

   You can delete the downloaded file afterwards. From a terminal, run
   `ppgmods install-app` (add `no-desktop` to skip the desktop shortcut) and
   `ppgmods uninstall-app`. Uninstalling leaves your mods and settings alone.

The window is a local web page. It opens in Edge or Chrome in app mode, or in
your default browser if neither is installed. The program serves it only to
your own PC (127.0.0.1), and every request needs a random per-session key.
Only one copy runs at a time: starting it again brings up the existing
window. It quits a few minutes after you close the window, unless a task is
still running. A log is kept in the data folder as `ppgmods.log`.

The window covers everything:

- **Browse mods**: search GameBanana and both Workshop mirrors, paste a link,
  select several, install. Results for the same Workshop item are merged
  across mirrors, and each card shows which mirrors have it and at which
  version. Click a mod to open its details: the
  description, size and dates, the mods it needs, and a **safety check** that
  downloads and scans it before you install. Install then puts it straight
  into your `Mods` folder (on Linux, usually
  `~/.local/share/Steam/steamapps/common/People Playground/Mods`). Valve
  deleted the Workshop preview images of the removed mods, so ppgmods shows
  each mod's own thumbnail from inside its archive. For small mods it fetches
  these in the background (this can be turned off in Settings).
- **Installed**: check for and apply safe updates, verify files, rollback,
  pin, remove.
- **Recover Workshop**: back up the Steam cache, restore the safe copies, or
  import a downloaded archive.
- **Safety**: what is checked and why.
- **Settings**: game folder (found through Steam automatically), cooldown,
  blocklist.

When a mod is refused, the window shows the scanner's findings. HIGH findings
and the cooldown can be overridden for that one install with
**Install anyway**. CRITICAL findings and the worm cutoff can't be overridden
from the window.

### Always the latest version

Every push to `main` builds Windows and Linux binaries with GitHub Actions and
publishes them as a new release, `v0.1.<build number>`. ppgmods checks for a
newer release when it starts and every 6 hours, and shows an **Update now**
banner. The update replaces the installed copy, so menu entries and shortcuts
keep working. Updating
downloads the new build, checks it against the release's `SHA256SUMS.txt`
and replaces the program. From a terminal, run `ppgmods self-update`.
Updates can only be found while the repository is public. A private
repository's releases aren't visible to users.

Build from source: `go build ./cmd/ppgmods` (Go 1.25+). Run `ppgmods help`
for the command line, which does the same things for scripting.

## Command line

```sh
# 1. Save whatever Steam still has cached, BEFORE Steam syncs
ppgmods backup-workshop --dest ppg-backup
ppgmods restore-workshop ppg-backup      # installs only items that pass every check

# 2. Find mods
ppgmods search "melee"

# 3. GameBanana: fully automatic
ppgmods install gb:655674                # or paste the gamebanana.com/mods/... URL

# 4. Skymods (Steam Workshop mirror), by Workshop ID: queue as many as you like
ppgmods install sky:3801154351 sky:2573469317 gb:655674
#    already have a file from modsbase? import it:
ppgmods import ~/Downloads/3801154351_Quick_Draw_Mod.zip --workshop-id 3801154351

# 5. Keep up to date
ppgmods update                           # dry run: shows what would change
ppgmods update --yes                     # applies updates that pass every check
ppgmods verify                           # detects files changed/injected after install
ppgmods rollback gb:655674               # bad update? restore the previous version (pins it)
ppgmods list | pin | unpin | remove

# Check anything without installing it
ppgmods scan some_mod.zip
```

### Automatic updates

`update --yes` is safe to schedule because it only applies updates that pass
every check below. Anything else is held back and printed.

- **Windows:** in Task Scheduler, create a daily task that runs
  `ppgmods-windows-amd64.exe update --yes`.
- **Linux:** add a cron line such as `0 18 * * * /path/to/ppgmods update --yes >> ~/ppgmods.log 2>&1`.

### Contraptions

Contraptions (saved builds) from GameBanana, True Workshop and the Workshop
mirrors are installed too. They go into the game's `Contraptions` folder as
`Contraptions/<name>/<name>.jaap`, with the `.json`, `.outline` and `.png` that
belong to it. Only those four file types are copied, since contraptions
contain no code. A contraption you saved yourself under the same name is never
overwritten. The Installed view has an **Open Contraptions folder** button,
and verify, rollback and remove work for contraptions the same way as for mods.

### Finding mods, and duplicates across sites

- **top-mods** is searched through its sitemap (every People Playground item,
  cached for 12 hours), not the site's own search, which covers every game.
  This finds mods the site search misses.
- The same Workshop item found on several sites shows as **one card**:
  Skymods, top-mods, and True Workshop uploads with the same name and author.
  Each card lists every site that has it.
- Search results appear per source as they arrive. Skymods can take 10–20 s.

### Choosing the newest copy

Mirrors and uploads can hold different versions of the same mod, and their
dates mislead: True Workshop shows upload dates, and top-mods' date is not
always the newest. ppgmods downloads each copy (at most four) and compares
the **`ModVersion` in its `mod.json`**. Quick Draw, for example: Skymods v4.0,
True Workshop v3.2 (uploaded later), top-mods v1.0. It installs the highest
version from before the worm cutoff, skipping copies the scanner flags.

A copy whose `mod.json` names a different Workshop item (`CreatorUGCIdentity`)
is ignored. The details view lists every copy with its date, `mod.json`
version and size, and you can pick one. From the command line, use
`--mirror skymods:<id>`, `--mirror topmods:<id>` or
`--mirror trueworkshop:<id>`.

### Mods you already installed

Mods and contraptions already in the game folders, put there by hand or
downloaded from the sites, are found and tracked automatically when the window
opens. **Find already-installed** in Installed, or `ppgmods find-installed`,
does the same on demand. Each one is identified by:

- the Workshop ID in its `mod.json` (`CreatorUGCIdentity`), or
- a True Workshop upload with the same name and author, or
- if neither, its folder (as a local copy).

Each one is scanned and fingerprinted, so it shows as installed on every card
that refers to it, gets update checks, and is covered by Verify. Nothing is
moved. Installing it again from any site replaces that copy instead of adding
a second one.

## Safety model

The worm spread because the Workshop pushed code to every subscriber
automatically, within hours. Each layer here targets part of that:

| Check | Default | Override |
|---|---|---|
| Steam-origin copies (Skymods, Workshop cache) revised on/after **2026-09-21** | refused | `--allow-after-cutoff` |
| Steam-origin copy with no provable revision date | refused | `--allow-after-cutoff` |
| GameBanana / unreviewed True Workshop file younger than the **cooldown** (gives the community and the site's scanner time to catch a bad upload; maintainer-reviewed True Workshop uploads skip it) | 48 h | `--cooldown 0` |
| True Workshop SHA-256 mismatch, or the site's own scan not `clean` | refused | none |
| GameBanana antivirus/analysis result not `clean` | refused | none |
| GameBanana MD5 mismatch | refused | none |
| Archive or file hash, Workshop ID or GameBanana ID on the [blocklist](blocklist/blocklist.json) | refused | none |
| **CRITICAL** scan findings | refused | `--allow-critical` |
| **HIGH** scan findings | refused | `--allow-high` |
| Update adds findings the installed version did not have | held | `--allow-new-findings` |
| Unsafe archives: path traversal, symlinks, >2 GiB unpacked, >20,000 entries | refused | none |

The scanner checks C# source after removing comments and decoding `\uXXXX`
identifier escapes:

- **CRITICAL**:
  - starting processes, network access, native interop and `unsafe`
  - the Steam Workshop upload API (how the worm spread), Steam friends/chat (how it spammed), Steam auth tickets
  - deleting files, the Windows registry
  - self-replication: writing `.cs` files, or writing under Mods, Workshop or Steam paths
  - shipped executables (`.dll`, `.exe` and similar) or binaries disguised with another extension
  - `mod.json` script paths that point outside the mod
- **HIGH**: any other Steamworks use, loading assemblies or code at runtime, looking up types by string name, base64 blobs, character-code or split-string obfuscation, reading user folders or environment variables, nested archives.
- **MEDIUM** (shown, not blocking): writing or listing files, opening URLs.

Visual Studio build output (`bin/`, `obj/`, `.vs/`) that some authors ship by
accident is removed before scanning. The game builds mods from the `.cs`
files listed in `mod.json`, so as far as I know it never reads those folders.

After installing, ppgmods records a SHA-256 for every file. `verify` reports
any installed file that has changed and any new file that appears in a mod
folder (the worm's infection method). It also scans mod folders that ppgmods
didn't install.

Tested against 40 current GameBanana mods: none was flagged CRITICAL once
build output was removed, and one was flagged HIGH. That one was an
achievement/stats cheat that uses split strings
(`"A" + "ss" + "embly"`) to get past the game's own checks.

## Reporting a malicious mod

Open a pull request that adds an entry to
[`blocklist/blocklist.json`](blocklist/blocklist.json):

```json
{ "sha256": "<hash of the archive or .cs file>", "reason": "worm variant, steals Steam tickets" }
{ "workshop_id": "1234567890", "reason": "..." }
{ "gamebanana_mod": 123456, "reason": "..." }
```

Every `ppgmods` run downloads the latest list from this repository. The list
can only block mods, never allow them, so a tampered copy can't let anything
extra through.

## Where data lives

The config folder holds `state.json` (installed mods and their file hashes),
`backups/` (previous versions for rollback), the cached blocklist and a
`staging/` area:

- Windows: `%AppData%\ppgmods`
- Linux: `~/.config/ppgmods`
- Override with the `PPGMODS_HOME` environment variable.

Installed mods go into `<game>/Mods/<Mod Name> [gb-<id>]` or
`[sky-<workshop id>]`.

## License

GPL-3.0. See [LICENSE](LICENSE).
