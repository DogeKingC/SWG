# Open Workshop

A replacement for the People Playground Steam Workshop that keeps what made it
useful (one place, one-click installs, updates) without what let the worm
spread: code pushed to every subscriber with nobody looking at it first.

Every mod here was **checked automatically and reviewed by a maintainer**
before anyone could install it, and its file is stored permanently under a
name that includes its checksum, so a published file can never be swapped.

## Install

In PPG Mod Manager, set **Site** to *Open Workshop* (or *All sites*). Updates
arrive through **Check for updates**. If the Open Workshop has to take a mod
down, everyone who has it is warned.

## Publish your mod

The easy way: in PPG Mod Manager, go to **Installed**, find your mod and click
**Share**. It packs the mod, drafts the submission and opens GitHub.

By hand:

1. Zip your mod folder (the one with `mod.json`), or your contraption folder
   (with the `.jaap`).
2. Put the zip somewhere with a **direct** download link, for example a
   release in your own GitHub repository.
3. Add `workshop/submissions/<name>/submission.json` in a pull request, where
   `<name>` is lowercase letters, digits and dashes:

```json
{
  "name": "Quick Draw Mod",
  "author": "51804",
  "kind": "mod",
  "version": "4.0",
  "description": "Draw weapons faster.",
  "tags": ["weapons", "utility"],
  "download": "https://github.com/you/quick-draw/releases/download/v4.0/QuickDraw.zip",
  "sha256": "the file's SHA-256 (sha256sum / Get-FileHash)",
  "workshop_id": "3801154351",
  "maintainers": ["your-github-username"]
}
```

`workshop_id` is optional: set it if this replaces your deleted Steam Workshop
item, and people who still have the old copy are offered your update.

The pull request is checked automatically (the file matches the checksum,
unpacks safely, is the kind it says, and the scanner finds nothing
dangerous). Then a maintainer reviews it. To update, change `version`,
`download` and `sha256` in a new pull request; only the listed
`maintainers` can change an entry. To take a mod down, set
`"withdrawn": true` and a `"withdrawn_reason"`.

Only submit mods you made, or that you have the author's permission to share.

## For maintainers

- Review the checks' summary on the pull request. Warnings (author name
  differs from `mod.json`, a Workshop ID claim, HIGH findings) need a human
  look. Read the code of anything the scanner flags.
- A file with a CRITICAL finding (for example a bundled DLL) can only be
  published if its SHA-256 is added to `approved-critical.txt` with a reason,
  in a separate pull request. Worm-like findings can never be published.
- Protect `main` (require a review, and require code-owner review for
  `workshop/` and `.github/`).
- Signing: run `go run ./cmd/workshop keygen`, put the public key in
  `internal/workshop/key.go` and the private key in the repository secret
  `WORKSHOP_SIGNING_KEY`. From then on the app refuses an unsigned index.
