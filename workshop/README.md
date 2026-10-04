# Open Workshop

A replacement for the People Playground Steam Workshop that keeps what made it
useful (one place, one-click installs, updates) without what let the worm
spread: code pushed to every subscriber with nothing checking it first.

Nobody has to review a submission by hand: a submission is checked
automatically and published within minutes if it passes. The checks are
strict, and anything they can't clear on their own waits for the
repository owner's approval instead of being published. Every published file
is stored permanently under a name that includes its checksum, and the index
records that checksum, so a published file can never be swapped.

## Install

In PPG Mod Manager, set **Site** to *Open Workshop*. Updates arrive through
**Check for updates**; the app fetches the index when it needs it, so new
mods appear without updating the app. Uploads nobody reviewed wait 48 hours
before the app installs them. If a mod is withdrawn, everyone who has it is
warned.

## Publish your mod

The easy way: in PPG Mod Manager, go to **Installed**, find your mod and click
**Share**. It packs the mod into a zip and opens the submission form with the
details filled in; drag the zip into the **File** box and submit.

By hand: open a new issue with the **Publish on the Open Workshop** form and
attach the zip of your mod folder (the one with `mod.json`) or contraption
folder (with the `.jaap`), or paste a direct download link.

To update, submit again with the same name and a higher version. To take a
mod down, comment `/withdraw <reason>` on its issue.

Only submit mods you made, or that you have the author's permission to share.

## The checks

Never published (fix and edit the issue):

- The file isn't a zip/7z/rar, is over 50 MB, or doesn't unpack safely.
- It isn't the kind the form says (a mod needs `mod.json`, a contraption a `.jaap`).
- The scanner finds something the worm did (Workshop upload API, Steam
  friends/chat, auth tickets, self-replication, rewriting game files, hidden
  base64 code, symlinks).
- `mod.json` claims a different Steam Workshop item than the form.
- The name belongs to another GitHub account, or the version isn't higher
  than the published one.
- More than 3 publications by one account in a day.

Published only with the owner's approval (comment `/approve <SHA-256>`):

- The GitHub account is younger than 30 days (or its age can't be checked).
- Any other HIGH or CRITICAL scanner finding (a bundled DLL, reflection,
  network access, …).
- Files that aren't mod content (allowed: `.cs`, `.json`, images, sounds,
  `.txt`/`.md`, `.jaap`, `.outline`, fonts, license/readme files).
- A Steam Workshop ID: it replaces that item for everyone who still has it,
  so the owner confirms the submitter is its author.
- The author name differs from `mod.json`.
- An update that does something the previous version didn't (a scanner rule
  that wasn't found before).
- Republishing a withdrawn mod.

Every night the published files are scanned again with the current scanner;
a file that a newer rule flags is withdrawn automatically.

## For the owner

Commands and labels on a submission issue (only the repository owner's
count):

- Comment `/approve <SHA-256>` (the first 12 characters are enough) to
  publish a submission that is waiting for approval. The bot's waiting
  message lists the file's SHA-256. The approval names the file you checked:
  if the author changed the file since, nothing is published.
- Comment `/review <SHA-256>` to mark the version published from that issue
  as reviewed (the app skips the 48-hour wait). The "Published" message
  lists its SHA-256.
- Label `withdrawn` takes it down; everyone who has it is warned.

The `approved` and `reviewed` labels no longer do anything: a label can't
say which file it vouches for, and the author can change the file between
your review and the click.

The workflow sets `published`, `needs-changes` and `needs-approval` itself.

### Emergency: a worm may be spreading

- **Freeze the Open Workshop:** Actions → workshop → Run workflow, action
  `pause`, a reason, and `since` (when it may have started: a date like
  `2026-10-01` or how long ago, like `72h`). Nothing is published or
  approved until you resume; withdrawing still works. In the app, nothing
  installs or updates from the Open Workshop, versions published since
  `since` are hidden and treated as withdrawn, and Verify names every
  installed copy of one. Apps see it within 15 minutes (their next index
  check). Lift it with action `resume`.
- **Stop installs from every site** (GameBanana, Steam copies, True
  Workshop, Nexus, Open Workshop, local files): Actions → workshop → Run
  workflow, action `pause-all`, with a reason. Browsing still works; nothing
  installs or updates, and the app shows your reason. Apps read it on their
  next action. Lift it with action `resume-all`. (It sets `"pause"` in
  `blocklist/blocklist.json` on `main`; to stop only some sites, edit that
  by hand: `{"sources": ["gb"], "reason": "..."}`, sources `gb`, `sky`, `tw`,
  `nx`, `ow`, `local` or `*`.) The blocklist can only refuse, so a tampered
  copy can never allow anything.

The index is signed: the private key is the repository secret
`WORKSHOP_SIGNING_KEY`, the public key is in `internal/workshop/key.go`, and
the app refuses an unsigned or altered index. To replace the key, see the
comment in `key.go`.

How it works: [workshop.yml](../.github/workflows/workshop.yml) runs
`cmd/workshop` on each submission. Issue text is only passed as data (never
run). Files go into the `workshop-files` release; `index.json` (and
`index.sig`) live on the `workshop-data` branch.
