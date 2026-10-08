# Deep check: run a mod's code on a test bench (plan)

Not built yet. The scanner reads what a mod *could* do; the deep check runs
its code briefly and reports what it *tries* to do, in seconds, without
starting the game.

## How

1. A small .NET program (the bench) loads the mod the way the game does:
   compiles its `.cs` scripts with Roslyn against the game's own DLLs (from
   the person's install: `People Playground_Data/Managed`), loads its DLLs,
   calls its entry point (`Main`), then the events the game calls (spawn,
   `Start`/`Update` a few frames) on stand-ins for game objects.
2. Every dangerous .NET call is hooked (Harmony) before the mod runs: files,
   network, processes, code loading, registry, Steam, native calls. A hooked
   call is logged and refused; nothing happens.
3. A second wall from the OS: no network and a throwaway folder (bubblewrap
   on Linux; a restricted token / AppContainer on Windows).
4. Result: "when run, it tried to: …" or "tried nothing", shown with the
   scan findings and kept with the installed item.

## Where it runs

- On the PC, Windows build first: People Playground is a Windows game (on
  Linux through Proton/Wine), so the bench runs where the game's Mono runs.
  ppgmods (Go) downloads the bench and a pinned .NET runtime once, checked
  against SHA-256 sums baked into ppgmods.
- Later in Open Workshop CI: needs stand-ins for the game's API (the game's
  DLLs can't be put on GitHub).

## Order

1. Prototype: run the Pennywise mod (01 STUDIO, uses ZeroOne's loader and
   updater) and list what it tries.
2. Windows bench + hooks; then the OS wall; then wire into Install/Preview.
3. Linux (Proton) path, then CI.

## Limits

Code that only acts later, or only in real gameplay, can stay quiet; no such
mod is known, and the FPS++ worms acted on load. A quiet run lowers the risk,
it doesn't prove a mod safe; with the scanner it covers far more than either.
