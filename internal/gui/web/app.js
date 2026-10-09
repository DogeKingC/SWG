"use strict";

// Session token: passed in the URL fragment by ppgmods, never sent to servers.
const TOKEN = (() => {
  const h = location.hash.slice(1);
  try {
    if (h) sessionStorage.setItem("ppgm-token", h);
    return h || sessionStorage.getItem("ppgm-token") || "";
  } catch { return h; }
})();
if (location.hash) history.replaceState(null, "", location.pathname);

const $ = (s) => document.querySelector(s);
const $$ = (s) => [...document.querySelectorAll(s)];

async function api(path, opts = {}) {
  const headers = { "X-Token": TOKEN, ...(opts.headers || {}) };
  let body = opts.body;
  if (body && !(body instanceof Blob) && typeof body !== "string") {
    body = JSON.stringify(body);
    headers["Content-Type"] = "application/json";
  }
  const r = await fetch(path, { method: opts.method || (body ? "POST" : "GET"), headers, body });
  const text = await r.text();
  let data = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = { error: text.trim() }; }
  if (!r.ok) throw new Error((data && data.error) || text || r.statusText);
  return data;
}

function el(tag, attrs = {}, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue;
    if (k === "class") e.className = v;
    else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else if (k === "style") e.setAttribute("style", v);
    else e.setAttribute(k, v === true ? "" : v);
  }
  for (const k of kids.flat()) if (k != null && k !== false) e.append(k.nodeType ? k : String(k));
  return e;
}

function toast(msg, ms = 3500, action) {
  const t = $("#toast");
  t.replaceChildren(el("span", {}, msg));
  if (action) t.append(el("button", { class: "btn btn-sm toast-btn", onclick: () => { t.hidden = true; action.run(); } }, action.label));
  t.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => (t.hidden = true), ms);
}

function dialog(title, body, extra) {
  $("#dlgTitle").textContent = title;
  const b = $("#dlgBody");
  b.replaceChildren(...[].concat(body));
  const x = $("#dlgExtra");
  x.hidden = !extra;
  x.disabled = false;
  x.onclick = null;
  if (extra) {
    x.textContent = extra.label;
    x.onclick = () => { $("#dialog").close(); extra.run(); };
  }
  $("#dialog").showModal();
}
$("#dlgClose").onclick = () => $("#dialog").close();

// riskDialog asks the person to type RISK_PHRASE before installing a mod
// with CRITICAL findings. The server refuses the override without it.
const RISK_PHRASE = "I accept the risk";
function riskDialog(name, findings, go) {
  const input = el("input", { type: "text", class: "risk-input", autocomplete: "off", spellcheck: "false", "aria-label": "Confirmation phrase" });
  const body = [
    el("p", {}, el("b", {}, name), " has CRITICAL findings: code that could harm your PC or your Steam account if it is malicious."),
    findings && findings.length ? el("ul", { class: "findings mono small" }, findings.filter((f) => f.startsWith("[CRITICAL")).map((f) => el("li", {}, f))) : null,
    el("div", { class: "warnbox" }, "Mods run with full access to your PC. Only continue if you know the author, have read the code, or got the mod from them directly. Findings that match what the worm did can't be accepted here."),
    el("p", {}, "Type ", el("code", {}, RISK_PHRASE), " to install it anyway:"),
    input,
  ];
  dialog("Accept the risk?", body, { label: "Install anyway", run: () => go(RISK_PHRASE) });
  const x = $("#dlgExtra");
  x.className = "btn btn-danger";
  x.disabled = true;
  input.oninput = () => (x.disabled = input.value.trim() !== RISK_PHRASE);
  input.focus();
}

// ---------- navigation ----------
function show(view) {
  $$(".nav").forEach((n) => n.classList.toggle("active", n.dataset.view === view));
  $$(".view").forEach((v) => (v.hidden = v.id !== "view-" + view));
  if (view === "installed") refreshState();
}
$$(".nav").forEach((n) => (n.onclick = () => show(n.dataset.view)));

// ---------- state ----------
let state = null;

let installedSig = "";
async function refreshState() {
  try {
    state = await api("/api/state");
  } catch (e) {
    $("#gameChip").textContent = "ppgmods is not responding";
    $("#gameChip").className = "chip chip-bad";
    return;
  }
  const p = state.paths;
  $("#versionChip").textContent = "ppgmods " + state.version;
  $("#aboutVersion").textContent = state.version;
  const chip = $("#gameChip");
  if (p.game) {
    chip.textContent = "● People Playground found";
    chip.className = "chip chip-ok";
    chip.title = p.game;
    chip.onclick = null;
  } else {
    chip.textContent = "Game not found: set folder";
    chip.className = "chip chip-bad";
    chip.title = p.game_error || "";
    chip.onclick = () => show("settings");
  }
  $("#installedCount").textContent = (state.installed || []).length;
  $$(".cutoff").forEach((e) => (e.textContent = state.cutoff));
  $("#cutoffDate").textContent = state.cutoff;
  $("#cooldownShow").textContent = Math.round(state.settings.cooldown_hours);
  document.documentElement.dataset.theme = state.settings.theme || "dark";
  $("#reppgBox").hidden = !(state.loader && state.loader.re_ppg);
  $("#cacheList").textContent = p.workshop && p.workshop.length
    ? "Found: " + p.workshop.join("\n")
    : "No Workshop cache found on this PC.";
  $("#backupBtn").disabled = !(p.workshop && p.workshop.length);
  if (!$("#restorePath").value && state.lastBackup) $("#restorePath").value = state.lastBackup;
  if (document.activeElement?.closest?.("#settingsForm") == null) {
    $("#setGame").value = state.settings.game || "";
    $("#setCooldown").value = Math.round(state.settings.cooldown_hours);
    $("#setOffline").checked = state.settings.offline;
    $("#setBgThumbs").checked = !state.settings.no_bg_thumbs;
    $("#setSkymodsCheck").checked = !state.settings.no_skymods_check;
    $("#setTheme").value = state.settings.theme || "dark";
  }
  $("#gameHint").textContent = p.game ? "Using: " + p.game : (p.game_error || "");
  $("#pathsInfo").textContent = [
    "Game:          " + (p.game || "not found"),
    "Mods folder:   " + (p.mods || "-"),
    "Contraptions:  " + (p.contraptions || "-"),
    "Steam libraries:\n  " + ((p.libraries || []).join("\n  ") || "none found"),
    "Workshop cache:\n  " + ((p.workshop || []).join("\n  ") || "none found"),
    "Downloads:     " + (p.downloads || "-"),
    "ppgmods data:  " + p.data,
  ].join("\n");
  // Re-draw the lists only when what's installed changed (this runs every
  // few seconds; re-drawing would reload every thumbnail).
  const sig = JSON.stringify((state.installed || []).map((m) => [m.key, m.name, m.missing, m.folders, m.scan_max, m.pinned, m.risk_accepted, m.author]));
  if (sig !== installedSig) {
    installedSig = sig;
    renderInstalled();
    markInstalledCards();
  }
  renderDesktop(state.desktop);
  renderNexus(state.nexus);
  renderSkymods(state.skymods);
  showNXMOffer(state.nxm_offer);
  if (state.job && state.job.running && !state.job.background && !lastJobId) lastJobId = state.job.id;
  if (!lastJobId) setJob(state.job);
}

// The Installed view shows either mods or contraptions.
let installedKind = "mod";
try { installedKind = localStorage.getItem("installedKind") || "mod"; } catch { /* default */ }
$$(".seg-btn[data-ikind]").forEach((b) => (b.onclick = () => {
  installedKind = b.dataset.ikind;
  try { localStorage.setItem("installedKind", installedKind); } catch { /* not saved */ }
  renderInstalled();
}));

function renderInstalled() {
  const list = $("#installedList");
  const all = state.installed || [];
  renderMissing(all);
  const isC = (m) => m.item_kind === "contraption";
  $("#countMods").textContent = all.filter((m) => !isC(m)).length;
  $("#countContraptions").textContent = all.filter(isC).length;
  $$(".seg-btn[data-ikind]").forEach((x) => x.classList.toggle("active", x.dataset.ikind === installedKind));
  const mods = all.filter((m) => isC(m) === (installedKind === "contraption"));
  renderProfiles();
  $("#profileBar").hidden = installedKind === "contraption";
  $("#openMods").hidden = installedKind === "contraption";
  $("#openContraptions").hidden = installedKind !== "contraption";
  if (!mods.length) {
    list.replaceChildren(el("div", { class: "empty" }, installedKind === "contraption"
      ? "No contraptions installed yet. Switch Browse to Contraptions to find some."
      : "No mods installed yet. Find mods under Browse, or recover them from your Workshop cache."));
    return;
  }
  list.replaceChildren(...mods.map((m) => {
    const kindBadge = m.kind === "GameBanana" ? "badge badge-gb" : m.kind === "Steam Workshop" ? "badge badge-sky" : m.kind === "01 STUDIO" ? "badge badge-01" : "badge";
    const actions = m.off
      ? el("div", { class: "item-actions" },
        el("button", { class: "btn btn-primary btn-sm", onclick: () => run({ action: "turn-on", key: m.key }, "Turning on " + m.name) }, "Turn on"),
        el("button", { class: "btn btn-sm", onclick: () => dialog("Remove " + m.name + "?", [el("p", {}, "This deletes its folders (kept in ppgmods-off while it's off).")],
          { label: "Remove", run: () => run({ action: "remove", key: m.key }, "Removing " + m.name) }) }, "Remove"))
      : m.quarantined
      ? el("div", { class: "item-actions" },
        el("button", { class: "btn btn-sm", onclick: () => dialog("Put " + m.name + " back?", [el("p", {}, "It goes back into your game folder and the game loads it again. Only do this if you have checked it.")],
          { label: "Release", run: () => run({ action: "release", key: m.key }, "Releasing " + m.name) }) }, "Release"),
        el("button", { class: "btn btn-sm", onclick: () => dialog("Remove " + m.name + "?", [el("p", {}, "This deletes its quarantined copy.")],
          { label: "Remove", run: () => run({ action: "remove", key: m.key }, "Removing " + m.name) }) }, "Remove"))
      : el("div", { class: "item-actions" },
        m.link ? el("button", { class: "btn btn-ghost btn-sm", onclick: () => api("/api/open?what=url&url=" + encodeURIComponent(m.link)) }, "Open page") : null,
        m.item_kind !== "contraption" ? el("button", { class: "btn btn-sm", title: "Move it out of the Mods folder so the game doesn't load it; Turn on puts it back", onclick: () => run({ action: "turn-off", key: m.key }, "Turning off " + m.name) }, "Turn off") : null,
        m.key.startsWith("gb:") ? el("button", { class: "btn btn-sm", onclick: () => run({ action: "pin", key: m.key, pinned: !m.pinned }, m.pinned ? "Resuming updates" : "Pinning") }, m.pinned ? "Unpin" : "Pin") : null,
        el("button", { class: "btn btn-sm", title: "Restore the version installed before the last update", onclick: () => run({ action: "rollback", key: m.key }, "Rolling back " + m.name) }, "Rollback"),
        el("button", { class: "btn btn-ghost btn-sm", title: "Share this on the Open Workshop", onclick: () => run({ action: "ow-share", key: m.key }, "Packing " + m.name) }, "Share"),
        el("button", { class: "btn btn-sm", title: "Move it out of the game folder so the game can't load it; nothing is deleted", onclick: () => dialog("Quarantine " + m.name + "?",
          [el("p", {}, "Its folder is moved out of the game folder, so the game can't load it. Nothing is deleted: Release puts it back.")],
          { label: "Quarantine", run: () => run({ action: "quarantine", key: m.key }, "Quarantining " + m.name) }) }, "Quarantine"),
        el("button", { class: "btn btn-sm", onclick: () => confirmRemove(m) }, "Remove"),
      );
    return el("div", { class: "item" },
      thumbImg({ ref: m.key, name: m.name, image: "" }, "item-thumb"),
      el("div", { class: "item-main" },
        el("div", { class: "item-name" }, m.name, " ", el("span", { class: kindBadge }, m.kind),
          m.adopted ? el("span", { class: "badge", title: "Installed without this app; found in your game folder" }, "found on this PC") : null,
          m.scan_max === "HIGH" || m.scan_max === "CRITICAL" ? el("span", { class: "badge badge-bad", title: "The scanner flagged this mod; run Verify for details" }, "scanner: " + m.scan_max) : null,
          m.missing ? el("span", { class: "badge badge-bad", title: "Its folder is gone from the game folder" }, "missing") : null,
          m.withdrawn ? el("span", { class: "badge badge-bad", title: "Withdrawn from the Open Workshop: " + m.withdrawn }, "withdrawn") : null,
          m.risk_accepted ? el("span", { class: "badge badge-bad", title: "You installed this despite CRITICAL findings" }, "risk accepted") : null,
          m.pinned ? el("span", { class: "badge" }, " pinned") : null,
          m.quarantined ? el("span", { class: "badge badge-bad", title: "In quarantine: " + m.quarantined + ". The game can't load it." }, "quarantined") : null,
          m.off ? el("span", { class: "badge", title: "Turned off: moved out of the Mods folder (into ppgmods-off), so the game doesn't load it" }, "off") : null),
        el("div", { class: "item-meta" },
          m.author ? "by " + m.author + " · " : "", m.key, " · installed ", (m.installed_at || "").slice(0, 10),
          m.revision && !m.revision.startsWith("0001") ? " · source date " + m.revision.slice(0, 10) : "",
          " · ", (m.folders || []).join(", "))),
      actions);
  }));
}

$("#revokeApprovals").onclick = () => dialog("Revoke Trust and run approvals?",
  [el("p", {}, "RE_PPG then asks again before running any mod you allowed to skip its security checks. Nothing else changes.")],
  { label: "Revoke", run: () => run({ action: "revoke-approvals" }, "Revoking Trust and run approvals") });

// Mod lists: export the installed mods as a file, or install a shared one.
$("#exportList").onclick = () => run({ action: "list-export" }, "Saving the mod list");
$("#importList").onclick = () => $("#importListFile").click();
$("#importListFile").onchange = async (e) => {
  const f = e.target.files[0];
  e.target.value = "";
  if (!f) return;
  if (f.size > 1048576) { toast("That file is too large to be a mod list"); return; }
  run({ action: "list-import", list: await f.text() }, "Installing the mods of the list");
};

// Profiles: saved sets of the mods that are on.
function renderProfiles() {
  const sel = $("#profileSel");
  const names = state.profiles || [];
  const keep = sel.value || state.profile || "";
  if (sel.dataset.names !== names.join("\0")) {
    sel.dataset.names = names.join("\0");
    sel.replaceChildren(el("option", { value: "" }, names.length ? "Choose a profile" : "No profiles yet"),
      ...names.map((n) => el("option", { value: n }, n)));
  }
  sel.value = names.includes(keep) ? keep : (names.includes(state.profile) ? state.profile : "");
}
$("#profileUse").onclick = () => {
  const n = $("#profileSel").value;
  if (n) run({ action: "profile-use", name: n }, "Switching to profile " + n);
};
$("#profileSave").onclick = () => {
  const input = el("input", { placeholder: "e.g. Gore pack", value: $("#profileSel").value || "" });
  dialog("Save profile", [el("p", {}, "Saves which mods are on now under this name. Switching to it later turns the other mods off (moved out of the Mods folder, nothing deleted) and these back on."), input],
    { label: "Save", run: () => { const n = input.value.trim(); if (n) run({ action: "profile-save", name: n }, "Saving profile " + n); } });
  input.focus();
};
$("#profileDelete").onclick = () => {
  const n = $("#profileSel").value;
  if (!n) return;
  dialog("Delete profile " + n + "?", [el("p", {}, "Only the saved list is deleted; your mods stay as they are.")],
    { label: "Delete", run: () => run({ action: "profile-delete", name: n }, "Deleting profile " + n) });
};

// renderMissing offers to restore tracked items whose folders disappeared
// (deleted by accident, by another program, or by malware).
function renderMissing(all) {
  const gone = all.filter((m) => m.missing);
  const b = $("#missingBanner");
  b.hidden = !gone.length;
  if (!gone.length) return;
  const names = gone.slice(0, 4).map((m) => m.name).join(", ") + (gone.length > 4 ? ` and ${gone.length - 4} more` : "");
  // Items found on this PC without a mod site behind them can't be downloaded again.
  const restorable = gone.filter((m) => !m.key.startsWith("local:"));
  const local = gone.length - restorable.length;
  b.replaceChildren(
    el("div", {}, el("b", {}, `${gone.length} installed item${gone.length > 1 ? "s are" : " is"} missing from your game folder`), ": " + names + "."),
    el("div", { class: "small" }, "If you didn't delete them, check your PC for malware before restoring: whatever deleted them could do it again."),
    local ? el("div", { class: "small" }, `${local} of them came from a file on this PC, not a mod site, so ppgmods can't download ${local > 1 ? "them" : "it"} again.`) : null,
    el("div", { class: "row", style: "margin-top:8px" },
      restorable.length ? el("button", { class: "btn btn-primary btn-sm", "data-busy": true, onclick: () => run({ action: "repair", refs: restorable.map((m) => m.key) }, `Restoring ${restorable.length} item(s)`) },
        restorable.length === gone.length ? "Restore them" : `Restore ${restorable.length} from their sites`) : null,
      el("button", { class: "btn btn-ghost btn-sm", onclick: () => dialog("Stop tracking?", [el("p", {}, "ppgmods forgets " + names + ". Nothing is deleted; their folders are already gone.")],
        { label: "Stop tracking", run: () => run({ action: "forget", refs: gone.map((m) => m.key) }, "Forgetting " + gone.length + " item(s)") }) }, "Stop tracking")),
  );
}

function confirmRemove(m) {
  dialog("Remove " + m.name + "?", [el("p", {}, "This deletes ", el("code", {}, (m.folders || []).join(", ")), " from your Mods folder.")],
    { label: "Remove", run: () => run({ action: "remove", key: m.key }, "Removing " + m.name) });
}

// ---------- jobs ----------
let lastJobId = 0;
let jobRunning = false;

async function run(req, label) {
  try {
    const j = await api("/api/action", { body: req });
    lastJobId = j.id;
    setJob(j, label);
  } catch (e) {
    toast(e.message);
  }
}

function setJob(j, label) {
  const s = $("#jobStatus");
  if (!j || (j.background && !j.running)) { s.textContent = "Ready"; s.className = "job-status"; return; }
  jobRunning = j.running;
  $$("button[data-busy]").forEach((b) => (b.disabled = j.running));
  if (j.running) {
    s.textContent = (label || j.name) + "…";
    s.className = "job-status running";
  } else {
    s.className = "job-status";
    s.textContent = j.ok ? "Last task: " + j.name + " finished" : "Last task: " + j.name + " " + (j.error || "failed");
  }
}

async function pollJob() {
  if (!lastJobId) return;
  let j;
  try { j = await api("/api/job"); } catch { return; }
  if (!j || j.id !== lastJobId) return;
  setJob(j);
  if (!j.running) {
    lastJobId = 0;
    jobDone(j);
    refreshState();
  }
}

function jobDone(j) {
  const sum = j.summary;
  if (j.name === "verify") return showVerify(j.problems || []);
  if (j.ok) {
    if (j.data && j.data.restart) {
      restartApp("", "Updated to " + j.data.version + ". Restarting…");
      return;
    }
    if (j.name === "find-installed") {
      toast(j.data && j.data.found !== "0" ? `Now tracking ${j.data.found} mod(s) that were already installed` : "No untracked mods found");
      return;
    }
    if (j.name === "install-app") {
      const running = state?.desktop?.installed;
      dialog("Installed", [el("p", {}, "PPG Mod Manager is installed at " + ((j.data && j.data.exe) || "") + "."),
        el("p", {}, state?.desktop?.platform === "windows" ? "Start it from the Start menu or the desktop shortcut. You can delete the file you downloaded." : "Start it from your application menu or the desktop shortcut. You can delete the file you downloaded.")],
        running ? null : { label: "Switch to the installed copy", run: () => restartApp("installed") });
      $("#dlgExtra").className = "btn btn-primary";
      return;
    }
    if (sum && j.name === "update") {
      toast(`${sum.Current} up to date, ${sum.OK} ${$("#applyUpdates").dataset.last === "apply" ? "updated" : "can update"}, ${sum.Refused} held back`
        + (sum.Manual ? `, ${sum.Manual} to update by hand on Nexus Mods` : "")
        + (sum.NotChecked ? `, ${sum.NotChecked} with nothing to check (the log says why)` : ""), 8000);
      return;
    }
    if (sum && j.name === "repair") { toast(`${sum.OK} restored, ${sum.Refused} refused, ${sum.Failed} could not be restored (see the log)`, 8000); refreshState(); return; }
    if (sum) { toast(`${sum.OK} installed, ${sum.Refused} refused, ${sum.Failed} errors`, 6000); return; }
    if (j.name === "backup" && j.data) { $("#restorePath").value = j.data.dest; toast("Backup saved. Now restore the safe copies (step 2)."); return; }
    if (j.name === "install") {
      const c = j.data && j.data.kind === "contraption";
      toast("Installed into " + ((j.data && (c ? j.data.contraptions_dir : j.data.mods_dir)) || (c ? "your Contraptions folder" : "your Mods folder")), 7000,
        { label: c ? "Open Contraptions folder" : "Open Mods folder", run: () => api("/api/open?what=" + (c ? "contraptions" : "mods")) });
      return;
    }
    if (j.name === "ow-share" && j.data) return showShare(j.data);
    if (j.name === "revoke-approvals" && j.data) { toast("Revoked " + j.data.revoked + " Trust and run approval(s); RE_PPG asks again before running those mods", 7000); return; }
    if (j.name === "list-export" && j.data) {
      toast("Saved the list of " + j.data.count + " item(s): share the file, and anyone can install the same mods with Import list", 8000,
        { label: "Show file", run: () => api("/api/open?what=exports") });
      return;
    }
    if (j.name === "remove" && j.data && j.data.others) {
      toast("Removed. Another copy is still in your Mods folder (" + j.data.others + "), not installed by ppgmods.", 10000,
        { label: "Open Mods folder", run: () => api("/api/open?what=mods") });
      return;
    }
    toast(j.name + " finished");
    return;
  }
  const body = [];
  if (j.reasons && j.reasons.length) {
    body.push(el("p", {}, "ppgmods did not install this mod:"));
    body.push(el("ul", {}, j.reasons.map((r) => el("li", {}, r.replace(/ \(override: [^)]*\)/, "")))));
  } else {
    body.push(el("p", {}, j.error || "Something went wrong."));
  }
  if (j.findings && j.findings.length) {
    body.push(el("p", { class: "small" }, "What the scanner found:"));
    body.push(el("ul", { class: "findings mono small" }, j.findings.map((f) => el("li", {}, f))));
  } else {
    body.push(el("p", { class: "small" }, "The log at the bottom has the full details."));
  }
  let extra = null;
  if (j.browser && j.browser.nxm) {
    dialog("Get it from Nexus Mods", [el("p", {}, "On the mod's Files tab, click ", el("b", {}, "Mod Manager Download"), ". ppgmods asks you to confirm, then downloads, scans and installs it.")],
      { label: "Open Files tab", run: () => api("/api/open?what=url&url=" + encodeURIComponent(j.browser.url)) });
    $("#dlgExtra").className = "btn btn-primary";
    return;
  }
  if (j.browser && j.retry) {
    body.splice(0, body.length,
      el("p", {}, "This copy has to be downloaded in your browser: " + j.browser.reason + "."),
      (j.browser.mirror || "").startsWith("01studio:")
        ? el("p", { class: "small" }, "No 01studio.dev account? Many 01 STUDIO mods are also on Nexus Mods, where a free account works: open the mod's details and pick its Nexus Mods or mirror copy instead.")
        : null,
      (j.browser.mirror || "").startsWith("nexus:") ? nexusAccountBox() : null,
      el("p", {}, j.browser.handoff
        ? "Open the download page and click Slow download: Nexus Mods hands the file straight to ppgmods, which scans and installs it. (If your browser asks whether to open the link with ppgmods, allow it.)"
        : "Open the download page, click its download button, and ppgmods will pick the file up from your Downloads folder and install it automatically."));
    dialog("Download in your browser", body, { label: "Open download page", run: () => run(j.retry, "Waiting for the browser download") });
    $("#dlgExtra").className = "btn btn-primary";
    return;
  }
  $("#dlgExtra").className = "btn btn-danger";
  if (j.retry && j.risk) {
    const name = ((j.retry.refs && j.retry.refs[0]) || (j.retry.path || "this mod").split(/[\\/]/).pop());
    extra = { label: "Accept the risk…", run: () => riskDialog(name, j.findings, (confirm) => run({ ...j.retry, confirm }, "Installing (risk accepted)")) };
  } else if (j.retry) {
    body.push(el("div", { class: "warnbox" }, "Only continue if you have read the findings above and trust this mod's author. Mods run with full access to your PC."));
    extra = { label: "Install anyway", run: () => run(j.retry, "Installing (override)") };
  }
  dialog(j.error === "refused" ? "Not installed" : "Task failed", body, extra);
}

function showVerify(probs) {
  const panel = $("#verifyPanel");
  panel.hidden = false;
  const bad = probs.filter((p) => p.bad);
  panel.replaceChildren(
    el("b", {}, bad.length ? `⚠ ${bad.length} problem(s) found` : "✓ All managed mods match their install fingerprints"),
    probs.length ? el("ul", {}, probs.map((p) => el("li", { class: p.bad ? "bad" : "" }, p.folder + ": " + p.issue))) : null,
    ...(() => {
      const keys = [...new Set(bad.filter((p) => p.key).map((p) => p.key))];
      return keys.length ? [el("button", { class: "btn btn-primary btn-sm", style: "margin-top:8px", "data-busy": true,
        onclick: () => run({ action: "repair", refs: keys }, `Restoring ${keys.length} item(s)`) },
        `Restore ${keys.length} damaged item${keys.length > 1 ? "s" : ""} from their source`)] : [];
    })(),
  );
  show("installed");
}

// ---------- log ----------
let logNext = 0;
async function pollLog() {
  let r;
  try { r = await api("/api/log?since=" + logNext); } catch { return; }
  logNext = r.next;
  if (!r.lines.length) return;
  const log = $("#log");
  const atBottom = log.scrollHeight - log.scrollTop - log.clientHeight < 30;
  log.textContent += r.lines.join("\n") + "\n";
  if (log.textContent.length > 400000) log.textContent = log.textContent.slice(-300000);
  if (atBottom) log.scrollTop = log.scrollHeight;
  if (jobRunning) $("#jobStatus").textContent = r.lines[r.lines.length - 1].slice(9);
}
$("#logToggle").onclick = () => {
  const log = $("#log");
  log.hidden = !log.hidden;
  $("#logToggle").textContent = log.hidden ? "Show log" : "Hide log";
  log.scrollTop = log.scrollHeight;
};

// ---------- browse ----------
let src = "all";
let page = 1;
let lastQuery = "";
const selected = new Map();

$("#srcSel").onchange = () => { src = $("#srcSel").value; search(lastQuery, 1); };

// Type, sort order and popularity period; remembered in this browser.
const browse = { kind: "mod", sort: "relevance", period: "week" };
try { Object.assign(browse, JSON.parse(localStorage.getItem("browse") || "{}")); } catch { /* defaults */ }
function syncBrowse() {
  $$(".kind-btn").forEach((x) => x.classList.toggle("active", x.dataset.kind === browse.kind));
  $("#sortSel").value = browse.sort;
  $("#periodSel").value = browse.period;
  $("#periodSel").hidden = browse.sort !== "popular";
  // 01 STUDIO only publishes mods.
  const forContraptions = ["all", "ow", "gb", "nx", "tw", "sky"];
  for (const o of $$("#srcSel option")) o.hidden = browse.kind === "contraption" && !forContraptions.includes(o.value);
  if (browse.kind === "contraption" && !forContraptions.includes(src)) src = "all";
  $("#srcSel").value = src;
  $("#q").placeholder = browse.kind === "contraption" ? "Search contraptions, e.g. tank, house, bridge" : "Search mods, e.g. melee, tank, zombie";
  try { localStorage.setItem("browse", JSON.stringify(browse)); } catch { /* not saved */ }
}
$$(".kind-btn").forEach((b) => (b.onclick = () => { browse.kind = b.dataset.kind; syncBrowse(); search(lastQuery, 1); }));
$("#sortSel").onchange = () => { browse.sort = $("#sortSel").value; syncBrowse(); search(lastQuery, 1); };
$("#periodSel").onchange = () => { browse.period = $("#periodSel").value; syncBrowse(); search(lastQuery, 1); };
syncBrowse();
$("#searchForm").onsubmit = (e) => { e.preventDefault(); search($("#q").value.trim(), 1); };
$("#moreBtn").onclick = () => search(lastQuery, page + 1);
$("#linkForm").onsubmit = (e) => {
  e.preventDefault();
  const v = $("#link").value.trim();
  if (!v) return;
  const ref = /^\d{6,12}$/.test(v) ? "sky:" + v : v;
  run({ action: "install", refs: [ref] }, "Installing " + ref);
  $("#link").value = "";
};

// search asks each source separately and shows results as they arrive:
// Skymods can take 10-20 s, GameBanana and True Workshop about one.
let searchSeq = 0;
let pageLists = null; // this page's results per source, for re-rendering

async function search(q, p) {
  lastQuery = q;
  page = p;
  const seq = ++searchSeq;
  const grid = $("#results");
  const errs = $("#searchErrors");
  const parts = src === "all"
    ? (browse.kind === "contraption" ? ["ow", "tw", "gb", "nx", "ws"] : ["ow", "tw", "gb", "ws", "s01", "nx"])
    : [src === "sky" ? "ws" : src];
  const pending = new Set(parts);
  const lists = { gb: [], tw: [], ws: [], s01: [], nx: [], ow: [] };
  const merged = new Set();
  const errors = [];
  const notes = new Set();
  const start = p === 1 ? 0 : grid.querySelectorAll(".mod").length;
  if (p === 1) grid.replaceChildren();
  const status = el("div", { class: "empty search-status" });
  grid.append(status);
  const render = () => {
    if (seq !== searchSeq) return;
    pageLists = lists;
    // A 01 STUDIO mod that the Workshop mirrors also have is one card.
    const wsByRef = new Map(lists.ws.map((x) => [x.ref, x]));
    const s01 = lists.s01.filter((x) => {
      const w = wsByRef.get(x.ref);
      if (!w) return true;
      if (!(w.mirrors || []).some((l) => l.startsWith("01 STUDIO"))) w.mirrors = [...(w.mirrors || []), ...(x.mirrors || ["01 STUDIO"])];
      if (!w.image) w.image = x.image;
      if (!w.author) w.author = x.author;
      if ((x.download_count || 0) > (w.download_count || 0)) w.download_count = x.download_count;
      return false;
    });
    // A Nexus Mods upload of a mod already shown (same name) joins its card.
    const byTitle = new Map([...lists.ws, ...s01].map((x) => [normTitle(x.name), x]));
    const nx = lists.nx.filter((x) => {
      const w = byTitle.get(normTitle(x.name));
      if (!w) return true;
      if (!(w.mirrors || []).some((l) => l.startsWith("Nexus"))) w.mirrors = [...(w.mirrors || []), "Nexus Mods" + (x.version ? " v" + x.version : "")];
      if ((x.download_count || 0) > (w.download_count || 0)) w.download_count = x.download_count;
      return false;
    });
    const shown = mergeShown(browse.sort, lists.ow, lists.tw.filter((x) => !merged.has(x.ref)), lists.gb, nx, lists.ws, s01);
    [...grid.querySelectorAll(".mod")].slice(start).forEach((c) => c.remove());
    status.before(...shown.map(card));
    const waiting = [...pending].map((x) => ({ gb: "GameBanana", tw: "True Workshop", ws: "Workshop mirrors (Skymods can be slow)", s01: "01 STUDIO", nx: "Nexus Mods", ow: "Open Workshop" }[x]));
    status.textContent = waiting.length ? "Still searching: " + waiting.join(", ") + "…" : (!shown.length && p === 1 ? "No mods found." : "");
    status.hidden = !status.textContent;
    errs.hidden = !errors.length;
    errs.textContent = errors.join(" · ");
    $("#searchNotes").hidden = !notes.size;
    $("#searchNotes").textContent = [...notes].join(" ");
    $("#moreBtn").hidden = pending.size > 0 || !shown.length;
    markInstalledCards();
  };
  render();
  await Promise.all(parts.map(async (part) => {
    try {
      const r = await api("/api/search?q=" + encodeURIComponent(q) + "&page=" + p + "&part=" + part +
        "&kind=" + browse.kind + "&sort=" + browse.sort + "&period=" + (browse.sort === "popular" ? browse.period : ""));
      (r.notes || []).forEach((n) => notes.add(n));
      if (part === "gb") lists.gb = r.gamebanana || [];
      if (part === "tw") lists.tw = r.trueworkshop || [];
      if (part === "ws") { lists.ws = r.workshop || []; (r.merged_tw || []).forEach((x) => merged.add(x)); }
      if (part === "s01") lists.s01 = r.studio01 || [];
      if (part === "nx") lists.nx = r.nexus || [];
      if (part === "ow") lists.ow = r.openworkshop || [];
      errors.push(...(r.errors || []));
    } catch (e) {
      errors.push(e.message);
    }
    pending.delete(part);
    render();
  }));
}

function interleave(...lists) {
  const out = [];
  const n = Math.max(...lists.map((l) => l.length));
  for (let i = 0; i < n; i++) for (const l of lists) if (l[i]) out.push(l[i]);
  return out;
}

function dateKey(s) {
  if (!s || s === "unknown") return 0;
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(s);
  if (m) return Date.UTC(+m[1], +m[2] - 1, +m[3]);
  const t = Date.parse(s);
  return Number.isNaN(t) ? 0 : t;
}

// Popular and recently updated are one ranking across sites: most downloads
// first, or newest date first. Relevance still mixes sources so one site
// does not fill the page.
function mergeShown(sort, ...lists) {
  const mixed = interleave(...lists);
  if (sort === "updated") {
    return mixed.slice().sort((a, b) => dateKey(b.date) - dateKey(a.date));
  }
  if (sort === "popular") {
    return mixed.slice().sort((a, b) => (b.download_count || 0) - (a.download_count || 0) || dateKey(b.date) - dateKey(a.date));
  }
  return mixed;
}

// sourceBadges labels where a result comes from (and, for True Workshop,
// whether its maintainers reviewed it).
// normTitle compares names across sites: "Jujutsu Playground [RELEASE]"
// and "Jujutsu Playground" are the same mod.
function normTitle(t) {
  return (t || "").toLowerCase().replace(/\[[^\]]*\]|\([^)]*\)/g, " ").replace(/\bv(er(sion)?)?\s*[:.]?\s*\d+(\.\d+)*\b/g, " ")
    .replace(/[^a-z0-9]+/g, " ").trim();
}

function sourceBadges(m) {
  if (m.ref.startsWith("gb:")) return [el("span", { class: "badge badge-gb" }, "GameBanana")];
  if (m.ref.startsWith("nx:")) return [el("span", { class: "badge badge-nx" }, "Nexus Mods")];
  if (m.ref.startsWith("ow:")) return [el("span", { class: "badge badge-ow" }, "Open Workshop"),
    m.reviewed ? el("span", { class: "badge badge-ok", title: "Checked automatically, and vouched for by the Open Workshop's owner" }, "✓ reviewed")
      : el("span", { class: "badge", title: "Passed the Open Workshop's automatic checks; nobody reviewed it by hand" }, "checked automatically")];
  if (m.ref.startsWith("tw:")) return [
    el("span", { class: "badge badge-tw" }, "True Workshop"),
    m.reviewed ? el("span", { class: "badge badge-ok", title: "Reviewed by True Workshop's maintainers" }, "✓ reviewed")
      : el("span", { class: "badge badge-warn", title: "Only passed True Workshop's automated scanner" }, "not reviewed"),
  ];
  const has01 = m.source === "01 STUDIO" || (m.mirrors || []).some((l) => l.startsWith("01 STUDIO"));
  const out = [];
  if (m.source !== "01 STUDIO") out.push(el("span", { class: "badge badge-sky" }, m.mirrors && m.mirrors.length > 1 ? m.mirrors.length + " mirrors" : "Workshop mirror"));
  if (has01) out.push(el("span", { class: "badge badge-01", title: "Published by 01 STUDIO on 01studio.dev" }, "01 STUDIO"));
  if ((m.mirrors || []).some((l) => l.startsWith("Nexus"))) out.push(el("span", { class: "badge badge-nx", title: "Also on Nexus Mods" }, "Nexus"));
  return out;
}

function thumbURL(u) {
  if (!u) return "";
  if (u.includes("steamusercontent.com")) return u + "?imw=480&imh=270&ima=fit&impolicy=Letterbox&letterbox=false";
  return u;
}

// thumbImg shows the Workshop/GameBanana preview image, then the thumbnail
// extracted from the mod archive (Valve deleted the Workshop images of the
// removed mods), then a placeholder.
function thumbImg(m, cls) {
  const box = el("div", { class: cls + " thumb-empty" }, el("span", {}, (m.name || "?").trim().charAt(0).toUpperCase()));
  const tries = [];
  if (m.image) tries.push(thumbURL(m.image));
  tries.push("/api/thumb?ref=" + encodeURIComponent(m.ref) + "&t=" + encodeURIComponent(TOKEN) + "&v=" + (thumbBust[m.ref] || 0));
  const img = el("img", { alt: "", loading: "lazy", decoding: "async" });
  let i = 0;
  img.onerror = () => {
    i++;
    if (i < tries.length) { img.src = tries[i]; return; }
    img.remove();
    if (cls === "thumb") queueThumb(m);
  };
  img.onload = () => { box.classList.remove("thumb-empty"); box.querySelector("span")?.remove(); };
  img.src = tries[0];
  box.append(img);
  return box;
}
const thumbBust = {};

// Background thumbnail fetching for Workshop mirror mods whose Steam image is
// gone: one small mod at a time, paused while a task runs.
const thumbQueue = [];
const thumbTried = new Set();
let thumbBusy = 0; // fetches running

function sizeBytes(s) {
  const m = /([\d.]+)\s*(KB|MB|GB|B)/i.exec(s || "");
  if (!m) return Infinity;
  return Number(m[1]) * { B: 1, KB: 1024, MB: 1048576, GB: 1073741824 }[m[2].toUpperCase()];
}

function queueThumb(m) {
  if (!m.ref.startsWith("sky:") || m.after_cutoff || thumbTried.has(m.ref)) return;
  if (state?.settings?.no_bg_thumbs || sizeBytes(m.size) > 3 * 1048576) return;
  thumbTried.add(m.ref);
  thumbQueue.push(m);
  pumpThumbs();
}

// Two at a time, and never while a task runs or a mod's details are open
// (their own check shouldn't queue behind thumbnails).
async function pumpThumbs() {
  if (thumbBusy >= 2 || !thumbQueue.length) return;
  if (jobRunning || state?.settings?.no_bg_thumbs || $("#details").open) { setTimeout(pumpThumbs, 2000); return; }
  thumbBusy++;
  const m = thumbQueue.shift();
  try {
    if (document.querySelector(`.mod[data-ref="${CSS.escape(m.ref)}"]`)) {
      const p = await api("/api/preview?ref=" + encodeURIComponent(m.ref));
      fillAuthor(m, p.author);
      if (p.thumb) {
        thumbBust[m.ref] = Date.now();
        $$(`.mod[data-ref="${CSS.escape(m.ref)}"] .thumb`).forEach((t) => t.replaceWith(thumbImg({ ...m, image: "" }, "thumb")));
      }
    }
  } catch { /* leave the placeholder */ }
  thumbBusy--;
  setTimeout(pumpThumbs, 500);
}

// fillAuthor adds an author learned from the mod's own mod.json to a card
// whose listing had none (Skymods and top-mods often omit it).
function fillAuthor(m, author) {
  if (!author || m.author) return;
  m.author = author;
  $$(`.mod[data-ref="${CSS.escape(m.ref)}"] .mod-meta`).forEach((d) => d.prepend("by " + author + " · "));
}

function card(m) {
  const isGB = m.ref.startsWith("gb:");
  const installed = installedKeys().has(m.ref);
  const pick = el("input", { type: "checkbox", class: "pick", title: "Select", "aria-label": "Select " + m.name });
  pick.checked = selected.has(m.ref);
  pick.disabled = m.after_cutoff || installed;
  const c = el("div", { class: "mod" + (pick.checked ? " selected" : ""), tabindex: "0", role: "button", "aria-label": "Details for " + m.name, "data-ref": m.ref },
    thumbImg(m, "thumb"), pick,
    el("div", { class: "mod-body" },
      el("div", { class: "mod-name" }, m.name),
      el("div", { class: "mod-meta" }, (m.author ? "by " + m.author + " · " : "") + (m.version ? "v" + m.version + " · " : "") + (isGB || m.ref.startsWith("nx:") || m.ref.startsWith("ow:") ? "updated " : m.ref.startsWith("tw:") ? "uploaded " : m.source === "01 STUDIO" ? "published " : "copied ") + m.date + (m.size ? " · " + m.size : "")),
      m.trend ? el("div", { class: "mod-trend" }, m.trend) : null,
      !isGB && m.mirrors && m.mirrors.length ? el("div", { class: "mod-mirrors", title: "Mirror copies found in this search" }, m.mirrors.join(" · ")) : null,
      el("div", { class: "mod-foot" },
        ...sourceBadges(m),
        m.kind === "contraption" ? el("span", { class: "badge badge-kind" }, "contraption") : null,
        m.category && m.category !== "Contraptions" ? el("span", { class: "badge" }, m.category) : null,
        m.after_cutoff ? el("span", { class: "badge badge-bad", title: "Revised after the worm started; refused" }, "after cutoff") : null,
        installed ? el("span", { class: "badge badge-ok" }, "installed") : null,
        installed || m.after_cutoff ? null : el("button", {
          class: "btn btn-primary btn-sm", "data-busy": true, disabled: jobRunning,
          onclick: (e) => { e.stopPropagation(); install(m.ref, m.name); },
        }, "Install"))));
  pick.onclick = (e) => e.stopPropagation();
  pick.onchange = () => {
    if (pick.checked) selected.set(m.ref, m.name); else selected.delete(m.ref);
    c.classList.toggle("selected", pick.checked);
    updateSelection();
  };
  c.onclick = () => openDetails(m);
  c.onkeydown = (e) => { if (e.key === "Enter" && e.target === c) openDetails(m); };
  if (!m.author) queueThumb(m); // the mod.json inside names the author
  return c;
}

function installedKeys() {
  const keys = new Set();
  for (const i of state?.installed || []) { keys.add(i.key); (i.aliases || []).forEach((a) => keys.add(a)); }
  return keys;
}

function markInstalledCards() {
  const keys = installedKeys();
  $$(".mod[data-ref]").forEach((c) => {
    if (!keys.has(c.dataset.ref) || c.querySelector(".badge-ok")) return;
    const foot = c.querySelector(".mod-foot");
    foot.querySelector(".btn")?.remove();
    foot.append(el("span", { class: "badge badge-ok" }, "installed"));
    const pick = c.querySelector(".pick");
    pick.checked = false; pick.disabled = true;
    c.classList.remove("selected");
    selected.delete(c.dataset.ref);
  });
  updateSelection();
}

function install(ref, name, override, mirror, confirm) {
  run({ action: "install", refs: [ref], override, mirror: mirror || "", confirm: confirm || "" }, "Installing " + (name || ref));
}

// ---------- details ----------
let detailsRef = null;
let detailsMirror = ""; // "" = automatic (newest clean copy)

async function openDetails(m, mirror) {
  const sameMod = detailsRef === m.ref && $("#details").open;
  detailsRef = m.ref;
  detailsMirror = mirror || "";
  const d = $("#details");
  const isGB = m.ref.startsWith("gb:");
  $("#detHero").replaceChildren(thumbImg(m, "det-hero"));
  $("#detTitle").textContent = m.name;
  $("#detSub").replaceChildren(...sourceBadges(m), m.author ? " by " + m.author : "");
  if (!sameMod) {
    $("#detFacts").replaceChildren();
    $("#detDesc").textContent = "Loading description…";
    $("#detRequired").replaceChildren();
    $("#detMirrors").replaceChildren();
  }
  $("#detCheck").replaceChildren(el("div", { class: "check-wait" }, el("span", { class: "spinner" }), "Downloading and scanning before install…"));
  setDetailActions(m, null);
  if (!d.open) d.showModal();
  if (!sameMod) d.querySelector(".det-scroll").scrollTop = 0;

  if (!sameMod) api("/api/details?ref=" + encodeURIComponent(m.ref) + "&name=" + encodeURIComponent(m.name || "")).then((v) => {
    if (detailsRef !== m.ref) return;
    const facts = [
      [isGB || m.ref.startsWith("nx:") ? "Updated" : m.ref.startsWith("tw:") ? "Uploaded" : "Version", v.revision || v.date],
      ["Likes", v.likes ? v.likes.toLocaleString() : ""],
      ["Size", v.size],
      ["Category", v.category],
      ["Downloads", v.downloads ? v.downloads.toLocaleString() : ""],
      ["Views", v.views ? v.views.toLocaleString() : ""],
      ["ID", m.ref],
    ].filter((f) => f[1]);
    $("#detFacts").replaceChildren(...facts.map(([k, val]) => el("div", { class: "fact" }, el("span", {}, k), el("b", {}, String(val)))));
    $("#detDesc").textContent = v.description || "No description.";
    if (v.after_cutoff) $("#detSub").append(" ", el("span", { class: "badge badge-bad" }, "revised after the worm cutoff"));
    if (v.required && v.required.length) {
      $("#detRequired").replaceChildren(
        el("h3", {}, "Needs these mods too"),
        el("div", { class: "req-list" }, v.required.map((r) => el("button", {
          class: "req", onclick: () => openDetails({ ref: "sky:" + r.workshop_id, name: r.title }),
        }, r.title, el("span", { class: "muted" }, " · sky:" + r.workshop_id)))),
        el("div", { class: "toolbar" }, el("button", {
          class: "btn btn-sm", onclick: () => run({ action: "install", refs: v.required.map((r) => "sky:" + r.workshop_id) }, "Installing required mods"),
        }, "Install all required mods")));
    }
    $("#detPage").onclick = () => api("/api/open?what=url&url=" + encodeURIComponent(v.page));
    if (v.mirrors && v.mirrors.length) renderMirrors(m, v.mirrors, null);
  }).catch((e) => { if (detailsRef === m.ref) $("#detDesc").textContent = "Could not load the description: " + e.message; });

  try {
    const p = await api("/api/preview?ref=" + encodeURIComponent(m.ref) + (detailsMirror ? "&mirror=" + encodeURIComponent(detailsMirror) : ""));
    if (detailsRef !== m.ref) return;
    if (p.mirrors && p.mirrors.length) renderMirrors(m, p.mirrors, p.chosen);
    showCheck(m, p);
  } catch (e) {
    if (detailsRef !== m.ref) return;
    $("#detCheck").replaceChildren(el("div", { class: "verdict verdict-bad" }, "Could not check this mod: " + e.message));
    setDetailActions(m, { verdict: "error" });
  }
}

// renderMirrors lists every mirror copy with its version; picking one
// re-runs the safety check on that copy and installs it.
function renderMirrors(m, mirrors, chosen) {
  const name = "mirror-" + m.ref;
  const row = (id, label, extra, disabled) => {
    const input = el("input", { type: "radio", name, value: id, disabled });
    input.checked = (detailsMirror || "") === id;
    input.onchange = () => openDetails(m, id);
    return el("label", { class: "mirror" + (disabled ? " mirror-off" : "") }, input, el("span", { class: "mirror-main" }, label), extra);
  };
  // Once the copies were downloaded, rank by mod.json version (dates can
  // mislead: True Workshop dates are upload dates).
  const versioned = mirrors.filter((x) => !x.after_cutoff && x.mod_version);
  const best = versioned.length ? versioned.reduce((a, b) => (cmpVer(b.mod_version, a.mod_version) > 0 ? b : a)) : null;
  const newestOK = best || mirrors.find((x) => !x.after_cutoff);
  const rows = [row("", "Automatic", el("span", { class: "muted small" }, "highest mod.json version from before the worm, skipping any the scanner flags"), false)];
  for (const mr of mirrors) {
    const tags = [];
    if (newestOK && mr.id === newestOK.id) tags.push(el("span", { class: "badge badge-ok" }, best ? "highest version" : "newest safe date"));
    if (mr.reviewed) tags.push(el("span", { class: "badge badge-tw" }, "reviewed"));
    if (mr.archived === "verified") tags.push(el("span", { class: "badge badge-ok", title: "Byte for byte the copy the pre-worm archive recorded before the worm" }, "✓ pre-worm archive"));
    else if (mr.archived === "recorded") tags.push(el("span", { class: "badge", title: "The pre-worm archive recorded this copy; it is checked against that record when downloaded" }, "in pre-worm archive"));
    if (mr.browser) tags.push(el("span", { class: "badge", title: "Downloaded in your browser, where you are signed in to 01studio.dev; ppgmods picks the file up from Downloads and scans it" }, "in your browser"));
    if (mr.gone) tags.push(el("span", { class: "badge badge-bad", title: "This mirror no longer has the file" }, "file gone"));
    if (mr.after_cutoff) tags.push(el("span", { class: "badge badge-bad" }, "after worm cutoff"));
    if (chosen && mr.id === chosen) tags.push(el("span", { class: "badge" }, "checked below"));
    rows.push(row(mr.id,
      el("span", {}, el("b", {}, mr.source), " · ", mr.version || "date unknown",
        mr.mod_version ? el("span", {}, " · mod ", el("b", {}, "v" + mr.mod_version))
          : mr.title_version ? el("span", { title: "Version from the title; mod.json not checked yet" }, " · title v" + mr.title_version) : "",
        mr.size ? " · " + mr.size : ""),
      el("span", { class: "mirror-tags" }, ...tags,
        el("button", { type: "button", class: "btn btn-ghost btn-sm", onclick: (e) => { e.preventDefault(); api("/api/open?what=url&url=" + encodeURIComponent(mr.page)); } }, "page")),
      mr.after_cutoff || mr.gone));
  }
  $("#detMirrors").replaceChildren(el("h3", {}, mirrors.length > 1 ? `Mirrors (${mirrors.length} copies)` : "Mirror"), el("div", { class: "mirrors" }, rows));
}

function cmpVer(a, b) {
  const pa = (a.match(/\d+/g) || []).map(Number), pb = (b.match(/\d+/g) || []).map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const x = pa[i] || 0, y = pb[i] || 0;
    if (x !== y) return x > y ? 1 : -1;
  }
  return 0;
}

function showCheck(m, p) {
  fillAuthor(m, p.author);
  if (p.thumb) {
    thumbBust[m.ref] = Date.now();
    $("#detHero").replaceChildren(thumbImg({ ...m, image: m.image }, "det-hero"));
    $$(`.mod[data-ref="${CSS.escape(m.ref)}"] .thumb`).forEach((t) => t.replaceWith(thumbImg(m, "thumb")));
  }
  const box = [];
  const text = {
    ok: ["verdict-ok", "✓ Passed every check. Ready to install."],
    review: ["verdict-warn", "⚠ Needs your review before installing."],
    blocked: ["verdict-bad", "✕ ppgmods will not install this mod."],
    risk: ["verdict-bad", "✕ CRITICAL findings. ppgmods won't install this unless you read them and accept the risk."],
    browser: ["verdict-warn", "This copy has to be downloaded in your browser."],
    unavailable: ["verdict-bad", "✕ This mod's mirror copy is gone."],
  }[p.verdict] || ["verdict-bad", p.verdict];
  box.push(el("div", { class: "verdict " + text[0] }, text[1]));
  if (p.verdict === "browser" && p.browser?.nxm) {
    box.push(el("p", { class: "small" }, "On the Files tab, click Mod Manager Download: your linked Nexus account lets ppgmods download, scan and install it."));
  } else if (p.verdict === "browser" && p.browser?.handoff) {
    box.push(el("p", { class: "small" }, "Reason: " + p.browser.reason + ". Nexus Mods then hands the file straight to ppgmods, which scans and installs it."));
  } else if (p.verdict === "browser") {
    box.push(el("p", { class: "small" }, "Reason: " + (p.browser?.reason || "unknown") + ". Open the download page in your browser and click download; ppgmods watches your Downloads folder and installs the file automatically."));
    if ((p.browser?.mirror || "").startsWith("01studio:")) box.push(el("p", { class: "small" }, "No 01studio.dev account? Pick the Nexus Mods or a mirror copy in the list above instead."));
    if ((p.browser?.mirror || "").startsWith("nexus:")) box.push(nexusAccountBox());
  }
  if (p.reasons && p.reasons.length && p.verdict !== "browser") {
    box.push(el("ul", { class: "small" }, p.reasons.map((r) => el("li", {}, r.replace(/ \(override: [^)]*\)/, "")))));
  }
  const info = [];
  if (p.chosen && p.mirrors) {
    const c = p.mirrors.find((x) => x.id === p.chosen);
    if (c) info.push(`checked the ${c.source} copy (version ${c.version})`);
  }
  if (p.kind === "contraption") info.push("contraption: " + (p.contraptions || []).join(", ") + " (goes in your Contraptions folder)");
  else if (p.files) info.push(`${p.files} files, ${p.scripts} C# scripts`);
  if (p.version) info.push("version " + p.version);
  if (p.author) info.push("mod.json author: " + p.author);
  if (p.scan_max) info.push("scanner: " + (p.scan_max === "none" ? "nothing found" : "highest " + p.scan_max));
  if (info.length) box.push(el("p", { class: "small muted" }, info.join(" · ")));
  if (p.can && p.scan_max) box.push(el("p", {}, el("b", {}, "What it can do: "),
    p.can.length ? p.can.join("; ") + "." : "nothing beyond the game, Unity and basic C#."));
  if (p.findings && p.findings.length) box.push(el("ul", { class: "findings mono small" }, p.findings.map((f) => el("li", {}, f))));
  if (p.description && $("#detDesc").textContent.startsWith("No description")) $("#detDesc").textContent = p.description;
  $("#detCheck").replaceChildren(...box);
  setDetailActions(m, p);
}

function setDetailActions(m, p) {
  const installed = installedKeys().has(m.ref);
  const btn = $("#detInstall");
  const warn = $("#detWarn");
  warn.hidden = true;
  btn.className = "btn btn-primary";
  btn.disabled = jobRunning;
  btn.onclick = () => { $("#details").close(); install(m.ref, m.name, null, detailsMirror); };
  btn.textContent = installed ? "Reinstall" : "Install";
  if (!p) { btn.textContent = installed ? "Reinstall" : "Install"; return; }
  if (p.verdict === "blocked" || p.verdict === "error" || p.verdict === "unavailable") { btn.disabled = true; return; }
  if (p.verdict === "browser" && p.browser?.nxm) {
    btn.textContent = "Open Files tab";
    btn.onclick = () => api("/api/open?what=url&url=" + encodeURIComponent(p.browser.url));
    return;
  }
  if (p.verdict === "browser") {
    btn.textContent = "Open download page";
    btn.onclick = () => { $("#details").close(); install(m.ref, m.name, { browser: true }, detailsMirror); };
    return;
  }
  if (p.verdict === "risk") {
    const over = { accept_risk: true };
    for (const r of p.reasons || []) if (r.includes("--cooldown")) over.skip_cooldown = true;
    btn.className = "btn btn-danger";
    btn.textContent = "Accept the risk…";
    warn.hidden = false;
    btn.onclick = () => { $("#details").close(); riskDialog(m.name, p.findings, (confirm) => install(m.ref, m.name, over, detailsMirror, confirm)); };
    return;
  }
  if (p.verdict === "review") {
    const over = {};
    for (const r of p.reasons || []) {
      if (r.includes("--allow-high")) over.allow_high = true;
      if (r.includes("--cooldown")) over.skip_cooldown = true;
      if (r.includes("--allow-new-findings")) over.allow_new_findings = true;
    }
    btn.className = "btn btn-danger";
    btn.textContent = "Install anyway";
    warn.hidden = false;
    btn.onclick = () => { $("#details").close(); install(m.ref, m.name, over, detailsMirror); };
  }
}
$("#detClose").onclick = () => { detailsRef = null; $("#details").close(); };
$("#details").addEventListener("close", () => (detailsRef = null));

function updateSelection() {
  $("#selectionBar").hidden = !selected.size;
  $("#selCount").textContent = selected.size + " selected";
}
$("#installSelected").onclick = () => {
  run({ action: "install", refs: [...selected.keys()] }, `Installing ${selected.size} mods`);
  selected.clear();
  $$(".mod.selected").forEach((c) => { c.classList.remove("selected"); c.querySelector(".pick").checked = false; });
  updateSelection();
};
$("#clearSelected").onclick = () => { selected.clear(); $$(".pick").forEach((p) => (p.checked = false)); $$(".mod.selected").forEach((c) => c.classList.remove("selected")); updateSelection(); };

// ---------- installed ----------
$("#checkUpdates").onclick = () => { $("#applyUpdates").dataset.last = "check"; run({ action: "update", apply: false }, "Checking for updates"); };
$("#applyUpdates").onclick = () => { $("#applyUpdates").dataset.last = "apply"; run({ action: "update", apply: true }, "Applying safe updates"); };
$("#verifyBtn").onclick = () => run({ action: "verify" }, "Verifying files");
$("#findBtn").onclick = () => run({ action: "find-installed" }, "Looking for mods installed without this app");
$("#openMods").onclick = () => api("/api/open?what=mods").catch((e) => toast(e.message));
$("#openContraptions").onclick = () => api("/api/open?what=contraptions").catch((e) => toast(e.message));
$("#openData").onclick = () => api("/api/open?what=data").catch((e) => toast(e.message));
["checkUpdates", "applyUpdates", "verifyBtn", "findBtn", "backupBtn", "restoreBtn", "importBtn"].forEach((id) => $("#" + id).setAttribute("data-busy", ""));

// ---------- recovery ----------
$("#backupBtn").onclick = () => run({ action: "backup" }, "Backing up the Workshop cache");
$("#restoreBtn").onclick = () => {
  const p = $("#restorePath").value.trim();
  if (!p) return toast("Back up first, or enter a backup folder");
  run({ action: "restore", path: p }, "Restoring from backup");
};
$("#importBtn").onclick = async () => {
  const f = $("#importFile").files[0];
  if (!f) return toast("Choose a .zip, .rar or .7z file first");
  const ws = $("#importWs").value.trim();
  if (ws && !/^\d{6,12}$/.test(ws)) return toast("Workshop ID should be digits only");
  toast("Uploading " + f.name + "…");
  try {
    const up = await api("/api/upload?name=" + encodeURIComponent(f.name), { method: "POST", body: f, headers: { "Content-Type": "application/octet-stream" } });
    run({ action: "import", path: up.path, workshop_id: ws }, "Importing " + f.name);
  } catch (e) { toast(e.message); }
};

// ---------- settings ----------
$("#settingsForm").onsubmit = async (e) => {
  e.preventDefault();
  try {
    await api("/api/settings", { body: {
      game: $("#setGame").value.trim(),
      cooldown_hours: Math.max(0, Number($("#setCooldown").value) || 0),
      offline: $("#setOffline").checked,
      no_bg_thumbs: !$("#setBgThumbs").checked,
      no_skymods_check: !$("#setSkymodsCheck").checked,
      theme: $("#setTheme").value,
    } });
    toast("Settings saved");
    document.activeElement.blur();
    refreshState();
  } catch (err) { toast(err.message); }
};

// ---------- Skymods Cloudflare check ----------
// When smods.ru serves its browser check, ppgmods passes it by itself with a
// background browser and retries - no interaction needed. The banner below
// only appears if the automatic attempt failed; the Settings panel shows
// what is going on and offers a manual retry.
let skyCheckPoll = null;
let skyDismissed = false;

function renderSkymods(sm) {
  if (!sm) return;
  const off = state.settings && state.settings.no_skymods_check;
  if (!sm.challenge) skyDismissed = false;
  const st = sm.check || {};
  const banner = $("#skyBanner");
  const stuck = sm.challenge && !sm.clearance && !st.running && (st.error || (st.at && !st.ok));
  banner.hidden = !(stuck && !skyDismissed);
  if (!banner.hidden) {
    $("#skyText").textContent = "smods.ru is blocking Skymods for now and the background browser check did not get through. It usually clears on its own; other sites keep working. You can retry from Settings.";
    $("#skyCheckBtn").disabled = st.running;
    $("#skyCheckBtn").textContent = st.running ? "Retry running…" : "Retry the check";
  }
  const box = $("#skymodsBox");
  if (!box) return;
  const lines = [
    el("p", { class: "small" },
      sm.challenge
        ? "smods.ru is showing its browser check right now. ppgmods passes it with a background browser and retries on its own - nothing for you to do."
        : "smods.ru is not asking for a browser check."),
    el("p", { class: "small" },
      sm.clearance
        ? "A clearance from the check is saved (earned " + (sm.clearance_age || "recently") + "). It is attached to Skymods requests when Cloudflare asks, and only ever sent to smods.ru."
        : "No clearance saved."),
  ];
  if (st.running) lines.push(el("p", { class: "small" }, "A background browser is passing the check right now…"));
  if (st.error) lines.push(el("p", { class: "small", style: "color:#ff8484" }, "Last check failed: " + st.error));
  else if (st.at && st.ok) lines.push(el("p", { class: "small muted" }, "Last check passed " + new Date(st.at).toLocaleTimeString() + "."));
  lines.push(el("div", { class: "toolbar" },
    el("button", { class: "btn btn-primary btn-sm", onclick: runSkyCheck, disabled: st.running === true }, st.running ? "Check running…" : "Run the check now"),
    sm.clearance ? el("button", { class: "btn btn-ghost btn-sm", onclick: clearSkyCheck }, "Clear saved check") : null,
  ));
  box.replaceChildren(...lines);
}

async function runSkyCheck() {
  skyDismissed = false;
  try {
    await api("/api/skymods", { method: "POST" });
    if (!skyCheckPoll) pollSkyCheck();
  } catch (e) { toast(e.message); }
}

function pollSkyCheck() {
  skyCheckPoll = setInterval(async () => {
    let sm;
    try { sm = await api("/api/skymods"); } catch { return; }
    state.skymods = sm;
    renderSkymods(sm);
    if (!sm.check || !sm.check.running) {
      clearInterval(skyCheckPoll);
      skyCheckPoll = null;
      if (sm.ok || (sm.check && sm.check.ok)) toast("Skymods browser check passed");
    }
  }, 1500);
}

async function clearSkyCheck() {
  try {
    await api("/api/skymods", { method: "DELETE" });
    toast("Cleared the saved Skymods check");
    refreshState();
  } catch (e) { toast(e.message); }
}

$("#skyCheckBtn").onclick = runSkyCheck;
$("#skyDismiss").onclick = () => { skyDismissed = true; $("#skyBanner").hidden = true; };

// ---------- desktop install ----------
function renderDesktop(d) {
  if (!d) return;
  const where = d.platform === "windows" ? "the Start menu" : "your application menu";
  let dismissed = false;
  try { dismissed = localStorage.getItem("ppgm-install-dismissed") === "1"; } catch {}
  const banner = $("#installBanner");
  banner.hidden = d.installed || dismissed;
  if (!d.installed) {
    $("#installText").textContent = d.copy_exists
      ? "PPG Mod Manager is installed on this PC, but you are running another copy. Install this copy over it, or switch to the installed one."
      : `Install PPG Mod Manager on this PC: it goes in ${where}, keeps itself up to date, and you can delete this download.`;
  }
  $("#appInfo").textContent = d.installed
    ? `Installed at ${d.install_path}. Start it from ${where}.`
    : `Running from ${d.running_from}. ${d.copy_exists ? "An installed copy exists at " + d.install_path + "." : "Not installed."}`;
  $("#appInstallBtn2").textContent = d.installed ? "Repair shortcuts" : "Install on this PC";
  $("#appUninstallBtn").hidden = !d.installed && !d.copy_exists;
}
function installApp() {
  run({ action: "install-app", apply: $("#installDesktop").checked }, "Installing PPG Mod Manager");
}
$("#installAppBtn").onclick = installApp;
$("#appInstallBtn2").onclick = installApp;
$("#installLater").onclick = () => { try { localStorage.setItem("ppgm-install-dismissed", "1"); } catch {} $("#installBanner").hidden = true; };
$("#appUninstallBtn").onclick = () => dialog("Uninstall PPG Mod Manager?", [
  el("p", {}, "This removes the program, its menu entry and its shortcuts, then closes this window."),
  el("p", {}, "Your installed mods, backups and settings stay where they are."),
], { label: "Uninstall", run: () => run({ action: "uninstall-app" }, "Uninstalling") });

// ---------- self update ----------
async function checkRelease(force) {
  let r;
  try { r = await api("/api/release" + (force ? "?force=1" : "")); } catch { return; }
  const latest = r.latest;
  if (r.error) {
    $("#aboutRelease").textContent = "Could not check for updates: " + r.error;
  } else if (latest && latest.newer) {
    $("#aboutRelease").textContent = latest.version + " is available.";
    $("#updateText").textContent = `ppgmods ${latest.version} is available (you have ${r.current}).`;
    $("#updateBanner").hidden = false;
  } else if (latest) {
    $("#aboutRelease").textContent = "You have the latest version (" + latest.version + ").";
  }
  if (force && r.error) toast("Could not check for updates");
}
$("#updateBtn").onclick = () => run({ action: "self-update" }, "Updating ppgmods");
$("#checkRelease").onclick = () => checkRelease(true);
$("#repoLink").onclick = (e) => { e.preventDefault(); api("/api/open?what=url&url=" + encodeURIComponent("https://github.com/Trlydev/SWG")); };
$("#quitBtn").onclick = async () => {
  await api("/api/quit", { body: {} }).catch(() => {});
  document.body.replaceChildren(el("div", { class: "empty", style: "margin:auto" }, "ppgmods has stopped. You can close this window."));
};

// restartApp restarts ppgmods. The new process takes over this window's
// address and token, so this window reloads into it; if it had to start
// somewhere else, it opens its own window and this one closes.
async function restartApp(which, msg) {
  const old = state?.version;
  await api("/api/action", { body: { action: "restart", key: which } }).catch(() => {});
  const note = el("div", { class: "empty", style: "margin:auto" }, msg || "Restarting PPG Mod Manager…");
  document.body.replaceChildren(note);
  await new Promise((r) => setTimeout(r, 1500)); // let the old process stop
  for (let i = 0; i < 60; i++) {
    try {
      const st = await api("/api/state");
      if (st && (st.version !== old || i > 6)) { location.reload(); return; }
    } catch { /* not up yet */ }
    await new Promise((r) => setTimeout(r, 500));
  }
  note.textContent = "PPG Mod Manager restarted in a new window.";
  window.close(); // works for app windows the program opened; otherwise the note stays
}

// ---------- Open Workshop ----------
// showShare walks an author through publishing on the Open Workshop: the zip
// is ready; they upload it, then propose the prefilled submission on GitHub,
// where it is checked automatically and published if it passes.
function showShare(d) {
  dialog("Share on the Open Workshop", [
    el("p", {}, "ppgmods packed it and filled in the submission form for you:"),
    el("ol", {},
      el("li", {}, "Click ", el("b", {}, "Open the form"), " (you need a GitHub account)."),
      el("li", {}, "Drag ", el("code", {}, d.zip.split(/[\\/]/).pop()), " into its File box. ",
        el("button", { class: "btn btn-ghost btn-sm", onclick: () => api("/api/open?what=share") }, "Show the zip")),
      el("li", {}, "Submit. It's checked automatically and, if it passes, published within minutes; the issue tells you."),
    ),
    el("p", { class: "small" }, "Only share mods you made or have the author's permission to share."),
  ], { label: "Open the form", run: () => api("/api/open?what=url&url=" + encodeURIComponent(d.issue_url)) });
  $("#dlgExtra").className = "btn btn-primary";
}

// ---------- Nexus Mods ----------
// renderNexus shows the account link in Settings. The key is typed once and
// never shown again (the server doesn't send it back).
let nexusShown = "";
function renderNexus(n) {
  n = n || {};
  const sig = JSON.stringify(n);
  if (sig === nexusShown) return;
  nexusShown = sig;
  const box = $("#nexusBox");
  const keyLink = el("a", { href: "#", onclick: (e) => { e.preventDefault(); api("/api/open?what=url&url=" + encodeURIComponent("https://www.nexusmods.com/users/myaccount?tab=api+access")); } }, "your Nexus account's API page");
  if (!n.linked) {
    const input = el("input", { type: "password", placeholder: "Personal API key", autocomplete: "off", spellcheck: "false" });
    const handler = el("input", { type: "checkbox", checked: false });
    box.replaceChildren(
      el("p", { class: "small" }, "Link your account to install from Nexus Mods without saving files by hand. Copy your personal API key from ", keyLink,
        " (at the bottom, \"Personal API Key\"). It stays on this PC and is only sent to Nexus Mods."),
      el("div", { class: "row" }, input, el("button", { class: "btn btn-primary", onclick: async () => {
        try {
          await api("/api/nexus", { body: { key: input.value.trim(), handler: handler.checked } });
          input.value = "";
          nexusShown = "";
          refreshState();
          toast("Nexus Mods account linked");
        } catch (e) { toast(e.message, 7000); }
      } }, "Link account")),
      el("label", { class: "check" }, handler, " Handle \"Mod Manager Download\" links",
        el("span", { class: "hint" }, "Only for nxm:// links (People Playground files on Nexus only have Manual download, which works without this). While this is on, Vortex or Mod Organizer don't get Nexus links, for other games either.")),
    );
    return;
  }
  const handler = el("input", { type: "checkbox", checked: !!n.handler, onchange: async () => {
    try { await api("/api/nexus", { body: { handler: handler.checked } }); nexusShown = ""; refreshState(); } catch (e) { toast(e.message, 7000); handler.checked = !handler.checked; }
  } });
  box.replaceChildren(
    el("p", {}, "Linked as ", el("b", {}, n.user), n.premium ? el("span", { class: "badge badge-ok" }, "premium") : el("span", { class: "badge" }, "free")),
    el("p", { class: "small" }, n.premium
      ? "Install downloads from Nexus Mods directly."
      : "Free accounts download through the site: Install opens the mod's download page; click Manual, then Slow download, and ppgmods picks the file up from your Downloads folder, checks it with Nexus Mods and installs it."),
    el("label", { class: "check" }, handler, " Handle \"Mod Manager Download\" links",
      el("span", { class: "hint" }, "While this is on, Vortex or Mod Organizer don't get Nexus links, for other games either; turning it off gives them back.")),
    el("div", {}, el("button", { class: "btn btn-ghost btn-sm", onclick: () => dialog("Unlink Nexus Mods?", [el("p", {}, "ppgmods forgets your API key and gives Nexus links back to the previous handler.")],
      { label: "Unlink", run: async () => { await api("/api/nexus", { method: "DELETE" }).catch((e) => toast(e.message)); nexusShown = ""; refreshState(); } }) }, "Unlink account")),
  );
}

// showNXMOffer asks before installing what a Nexus link sent: any website
// can open such a link, so it never installs by itself.
let nxmShownAt = "";
function showNXMOffer(o) {
  if (!o || o.at === nxmShownAt) return;
  nxmShownAt = o.at;
  dialog("Install from Nexus Mods?", [
    el("p", {}, "Nexus Mods sent ", el("b", {}, o.name), " to ppgmods."),
    el("p", { class: "small" }, "It will be downloaded and scanned like any mod before it is installed. If you didn't just click Mod Manager Download, close this."),
  ], { label: "Install", run: () => run({ action: "nxm", path: o.url }, "Installing " + o.name + " from Nexus Mods") });
  $("#dlgExtra").className = "btn btn-primary";
  $("#dlgClose").onclick = () => { $("#dialog").close(); api("/api/nxm-dismiss", { body: {} }).catch(() => {}); $("#dlgClose").onclick = () => $("#dialog").close(); };
}

// ---------- start ----------
(async function start() {
  await refreshState();
  search("", 1);
  checkRelease(false);
  pollLog();
  setInterval(pollLog, 1000);
  setInterval(pollJob, 800);
  setInterval(() => { if (!jobRunning && !document.hidden) refreshState(); }, 4000);
  const ping = () => api("/api/ping").catch(() => {});
  ping();
  setInterval(ping, 20000);
  setInterval(() => checkRelease(false), 30 * 60 * 1000);
})();

// nexusAccountBox: Nexus Mods only lets signed-in users download (a free
// account works), so offer its sign-in / sign-up page.
function nexusAccountBox() {
  return el("p", { class: "small" }, "Nexus Mods only lets signed-in users download (a free account works; free downloads are slower). Not signed in on nexusmods.com yet? ",
    el("button", { class: "btn btn-ghost btn-sm", onclick: () => api("/api/open?what=url&url=" + encodeURIComponent("https://users.nexusmods.com/")) }, "Sign in / create a free account"));
}
