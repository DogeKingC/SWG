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

function toast(msg, ms = 3500) {
  const t = $("#toast");
  t.textContent = msg;
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
  x.onclick = null;
  if (extra) {
    x.textContent = extra.label;
    x.onclick = () => { $("#dialog").close(); extra.run(); };
  }
  $("#dialog").showModal();
}
$("#dlgClose").onclick = () => $("#dialog").close();

// ---------- navigation ----------
function show(view) {
  $$(".nav").forEach((n) => n.classList.toggle("active", n.dataset.view === view));
  $$(".view").forEach((v) => (v.hidden = v.id !== "view-" + view));
  if (view === "installed") refreshState();
}
$$(".nav").forEach((n) => (n.onclick = () => show(n.dataset.view)));

// ---------- state ----------
let state = null;

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
  $("#cacheList").textContent = p.workshop && p.workshop.length
    ? "Found: " + p.workshop.join("\n")
    : "No Workshop cache found on this PC.";
  $("#backupBtn").disabled = !(p.workshop && p.workshop.length);
  if (!$("#restorePath").value && state.lastBackup) $("#restorePath").value = state.lastBackup;
  if (document.activeElement?.closest?.("#settingsForm") == null) {
    $("#setGame").value = state.settings.game || "";
    $("#setCooldown").value = Math.round(state.settings.cooldown_hours);
    $("#setOffline").checked = state.settings.offline;
  }
  $("#gameHint").textContent = p.game ? "Using: " + p.game : (p.game_error || "");
  $("#pathsInfo").textContent = [
    "Game:          " + (p.game || "not found"),
    "Mods folder:   " + (p.mods || "-"),
    "Steam libraries:\n  " + ((p.libraries || []).join("\n  ") || "none found"),
    "Workshop cache:\n  " + ((p.workshop || []).join("\n  ") || "none found"),
    "Downloads:     " + (p.downloads || "-"),
    "ppgmods data:  " + p.data,
  ].join("\n");
  renderInstalled();
  if (state.job && state.job.running && !lastJobId) lastJobId = state.job.id;
  if (!lastJobId) setJob(state.job);
}

function renderInstalled() {
  const list = $("#installedList");
  const mods = state.installed || [];
  if (!mods.length) {
    list.replaceChildren(el("div", { class: "empty" }, "Nothing installed yet. Find mods under Browse mods, or recover them from your Workshop cache."));
    return;
  }
  list.replaceChildren(...mods.map((m) => {
    const kindBadge = m.kind === "GameBanana" ? "badge badge-gb" : m.kind === "Steam Workshop" ? "badge badge-sky" : "badge";
    const actions = el("div", { class: "item-actions" },
      m.link ? el("button", { class: "btn btn-ghost btn-sm", onclick: () => api("/api/open?what=url&url=" + encodeURIComponent(m.link)) }, "Open page") : null,
      m.key.startsWith("gb:") ? el("button", { class: "btn btn-sm", onclick: () => run({ action: "pin", key: m.key, pinned: !m.pinned }, m.pinned ? "Resuming updates" : "Pinning") }, m.pinned ? "Unpin" : "Pin") : null,
      el("button", { class: "btn btn-sm", title: "Restore the version installed before the last update", onclick: () => run({ action: "rollback", key: m.key }, "Rolling back " + m.name) }, "Rollback"),
      el("button", { class: "btn btn-sm", onclick: () => confirmRemove(m) }, "Remove"),
    );
    return el("div", { class: "item" },
      el("div", {},
        el("div", { class: "item-name" }, m.name, " ", el("span", { class: kindBadge }, m.kind), m.pinned ? el("span", { class: "badge" }, " pinned") : null),
        el("div", { class: "item-meta" },
          m.key, " · installed ", (m.installed_at || "").slice(0, 10),
          m.revision && !m.revision.startsWith("0001") ? " · source date " + m.revision.slice(0, 10) : "",
          " · ", (m.folders || []).join(", "))),
      actions);
  }));
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
  if (!j) { s.textContent = "Ready"; s.className = "job-status"; return; }
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
      dialog("Updated to " + j.data.version, el("p", {}, "Close ppgmods and start it again to use the new version."));
      return;
    }
    if (sum && j.name === "update") {
      toast(`${sum.Current} up to date, ${sum.OK} ${$("#applyUpdates").dataset.last === "apply" ? "updated" : "can update"}, ${sum.Refused} held back`, 6000);
      return;
    }
    if (sum) { toast(`${sum.OK} installed, ${sum.Refused} refused, ${sum.Failed} errors`, 6000); return; }
    if (j.name === "backup" && j.data) { $("#restorePath").value = j.data.dest; toast("Backup saved. Now restore the safe copies (step 2)."); return; }
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
  if (j.retry) {
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

$$(".seg-btn").forEach((b) => (b.onclick = () => {
  src = b.dataset.src;
  $$(".seg-btn").forEach((x) => x.classList.toggle("active", x === b));
  search(lastQuery, 1);
}));
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

async function search(q, p) {
  lastQuery = q;
  page = p;
  const grid = $("#results");
  if (p === 1) grid.replaceChildren(el("div", { class: "empty" }, "Searching…"));
  let r;
  try { r = await api("/api/search?q=" + encodeURIComponent(q) + "&page=" + p); }
  catch (e) { grid.replaceChildren(el("div", { class: "empty" }, e.message)); return; }
  const errs = $("#searchErrors");
  errs.hidden = !(r.errors && r.errors.length);
  errs.textContent = (r.errors || []).join(" · ");
  let items = [];
  if (src !== "sky") items = items.concat(r.gamebanana || []);
  if (src !== "gb") items = items.concat(r.skymods || []);
  if (src === "all") items = interleave(r.gamebanana || [], r.skymods || []);
  if (p === 1) grid.replaceChildren();
  if (!items.length && p === 1) grid.append(el("div", { class: "empty" }, "No mods found."));
  grid.append(...items.map(card));
  $("#moreBtn").hidden = !items.length;
}

function interleave(a, b) {
  const out = [];
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    if (a[i]) out.push(a[i]);
    if (b[i]) out.push(b[i]);
  }
  return out;
}

function thumbURL(u) {
  if (!u) return "";
  if (u.includes("steamusercontent.com")) return u + "?imw=480&imh=270&ima=fit&impolicy=Letterbox&letterbox=false";
  return u;
}

function card(m) {
  const isGB = m.ref.startsWith("gb:");
  const installed = (state?.installed || []).some((i) => i.key === m.ref);
  const thumb = m.image
    ? el("div", { class: "thumb", style: `background-image:url("${thumbURL(m.image).replace(/"/g, "%22")}")` })
    : el("div", { class: "thumb thumb-empty" }, "◇");
  const pick = el("input", { type: "checkbox", class: "pick", title: "Select", "aria-label": "Select " + m.name });
  pick.checked = selected.has(m.ref);
  pick.disabled = m.after_cutoff || installed;
  const c = el("div", { class: "mod" + (pick.checked ? " selected" : "") }, thumb, pick,
    el("div", { class: "mod-body" },
      el("div", { class: "mod-name" }, m.name),
      el("div", { class: "mod-meta" }, (m.author ? "by " + m.author + " · " : "") + (isGB ? "updated " : "revised ") + m.date + (m.size ? " · " + m.size : "")),
      el("div", { class: "mod-foot" },
        el("span", { class: isGB ? "badge badge-gb" : "badge badge-sky" }, isGB ? "GameBanana" : "Workshop mirror"),
        m.category ? el("span", { class: "badge" }, m.category) : null,
        m.after_cutoff ? el("span", { class: "badge badge-bad", title: "Revised after the worm started; refused" }, "after cutoff") : null,
        installed ? el("span", { class: "badge badge-ok" }, "installed") : null,
        installed || m.after_cutoff ? null : el("button", {
          class: "btn btn-primary btn-sm", "data-busy": true, disabled: jobRunning,
          onclick: () => run({ action: "install", refs: [m.ref] }, "Installing " + m.name),
        }, "Install"))));
  pick.onchange = () => {
    if (pick.checked) selected.set(m.ref, m.name); else selected.delete(m.ref);
    c.classList.toggle("selected", pick.checked);
    updateSelection();
  };
  c.querySelector(".mod-name").onclick = () => api("/api/open?what=url&url=" + encodeURIComponent(m.url));
  c.querySelector(".mod-name").style.cursor = "pointer";
  c.querySelector(".mod-name").title = "Open the mod's page";
  return c;
}

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
$("#openMods").onclick = () => api("/api/open?what=mods").catch((e) => toast(e.message));
$("#openData").onclick = () => api("/api/open?what=data").catch((e) => toast(e.message));
["checkUpdates", "applyUpdates", "verifyBtn", "backupBtn", "restoreBtn", "importBtn"].forEach((id) => $("#" + id).setAttribute("data-busy", ""));

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
    } });
    toast("Settings saved");
    document.activeElement.blur();
    refreshState();
  } catch (err) { toast(err.message); }
};

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
$("#repoLink").onclick = (e) => { e.preventDefault(); api("/api/open?what=url&url=" + encodeURIComponent("https://github.com/DogeKingC/SWG")); };
$("#quitBtn").onclick = async () => {
  await api("/api/quit", { body: {} }).catch(() => {});
  document.body.replaceChildren(el("div", { class: "empty", style: "margin:auto" }, "ppgmods has stopped. You can close this window."));
};

// ---------- start ----------
(async function start() {
  await refreshState();
  search("", 1);
  checkRelease(false);
  pollLog();
  setInterval(pollLog, 1000);
  setInterval(pollJob, 800);
  setInterval(() => { if (!jobRunning) refreshState(); }, 15000);
})();
