# The FPS++ worms: analysis and how ppgmods stops them

Two People Playground Workshop worms, analysed from public samples:

- **FPS++** (February 2026), sample: [sheepism/FPSPlusPlus-malware](https://github.com/sheepism/FPSPlusPlus-malware)
- **FPS+++++** (September 2026, the one that got Workshop C# mods deleted),
  sample: [M4-77/FPSPlusPlusPlusPlusPlus](https://github.com/M4-77/FPSPlusPlusPlusPlusPlus),
  write-up: [redthefirst.github.io/SteamWorkshopMalware](https://redthefirst.github.io/SteamWorkshopMalware/)

The samples were read as text only, never built or run. No worm code is in
this repository; the scanner's tests use neutralized copies (same structure,
fake payloads).

## FPS++ (February)

A mod named "FPS++" by "Microsoft Word" (Workshop 3603531358). Its
`script.cs` loads a bundled `FPSPlusPlus.dll` through reflection
(`Type.GetType("System.Reflection.Assembly, mscorlib")` + `InvokeMember("Load")`)
so the game's checks never see `Assembly.Load`. The DLL:

1. republishes **every Workshop item the player made** with the worm's files
   (`Query.Items.WhereUserPublished` → `Edit().WithContent(...)`), sometimes
   adding "optimized!" to the description, and uploads a new public copy
   (`Editor.NewCommunityFile`); it upvotes and favourites all of them;
2. resets Steam achievements and stats, deletes `config.json`,
   `ControlScheme.json`, `CompiledModAssemblies`, `Maps`, `Contraptions`,
   PlayerPrefs and mod settings, disables every other mod;
3. **turns off the game's "reject shady code" protection** and forces a
   high frame-rate limit, then shows three times the real FPS so the game
   looks "optimized".

## FPS+++++ (September)

It rode in on hijacked popular mods ("Amy" Atomics 3454540789 and Vanilla
Tools 3468095876) and works in three stages.

**Stage 1, in the mod's script.** The source never names `Assembly.Load`.
It uses one of four rotating templates:

| | Trick |
|---|---|
| A | builds a `BinaryFormatter` with `JsonConvert.DeserializeObject("{}", Type.GetType("…BinaryFormatter"))` (Json.NET is allowed in mods), then deserializes a blob of serialized delegates that are `Assembly.Load(byte[])` and `Type.InvokeMember` |
| B | deserializes a **UnityEvent** from JSON whose persistent call is `System.IO.File, mscorlib` → `WriteAllBytes` (private fields filled through `DefaultMembersSearchFlags = NonPublic`), writes its DLL into `People Playground_Data/Managed`, and calls the entry point with a second UnityEvent |
| C | the same delegates as A, through `System.Security.Claims.ClaimsIdentity`'s bootstrap context, so the word BinaryFormatter never appears |
| D | the wrapper: random namespace and class names, `OnLoad()` + `Main()`, and a fake "Originally uploaded by …" comment |

The payload itself is a Windows DLL embedded as base64 (starting `TVqQ`,
"MZ"), split into hundreds of string variables.

**Stage 2** inflates and loads **stage 3**, which:

- infects other mods by writing new scripts from the templates above into
  their folders, and republishes them on the Workshop with comments like
  "hotfix", "rollback", "the mod is not infected";
- wipes in-game content, contraptions and stats; deletes `CompiledMods`;
- kills Discord, copies the Steam `config` folder into Discord's folder,
  deletes Steam's config, and **uploads the Discord folder (with the login
  token) to the Workshop**;
- collects machine details (user and machine name, CPU, GPU, RAM, MAC, IP
  from api.ipify.org);
- deletes browser profiles (Chrome, Edge, Firefox) and personal files;
- shows insulting and fake "protection service" messages, and a fake crash.

If you ran it: reset your Discord and Steam passwords, delete
`People Playground/CompiledMods`, remove the infected mods, and verify the
game's files in Steam.

## What ppgmods does about it

Scanner rules (worm-class rules can never be accepted in the window):

| Rule | Catches | Worm-class |
|---|---|---|
| `deserialization` | `IFormatter`, `BinaryFormatter`, `SerializationInfo`, `ClaimsIdentity`, serialized-delegate data (A, C) | yes |
| `json-gadget` | Json.NET deserializing into a `UnityEvent` or a type named by a string; `DefaultMembersSearchFlags`; `TypeNameHandling`; UnityEvent call lists in strings (A, B) | yes |
| `embedded-executable` | base64 literals that decode to a Windows program or DLL, also when split across many variables | yes |
| `game-path-tamper` | strings naming `…_Data/Managed`, `CompiledMods`, `CompiledModAssemblies` (B) | yes |
| `disables-protection` | touching `RejectShadyCode` (FPS++) | yes |
| `worm-dll` | bundled DLLs that reference the worms' Workshop republishing, protection switch, deserialization tricks, Discord/Steam theft | yes |
| `code-loader` | `Assembly.Load` and reflection on the Assembly type (FPS++'s loader) | no: real mods (e.g. Better Recompile) load their own DLLs; it still needs the typed confirmation |

Base64 literals are decoded (one by one, and joined in order) and checked
for the same names, since the worm hid them there.

Also:

- the samples' files are on the [blocklist](../blocklist/blocklist.json) by
  SHA-256, and FPS++'s Workshop item by ID;
- **Verify files** checks the game's `CompiledMods`, `CompiledModAssemblies`
  and `People Playground_Data/Managed` folders for infected DLLs;
- the Open Workshop runs the same rules on every submission and re-scans
  published files nightly;
- Workshop mirror copies from after the worm cutoff are refused, and copies
  that changed since the pre-worm archive recorded them are refused.
