const $ = (sel, el = document) => el.querySelector(sel);

async function api(path, opts = {}) {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json", ...(opts.headers || {}) },
    ...opts,
  });
  if (res.status === 204) return null;
  const text = await res.text();
  let body = null;
  try { body = text ? JSON.parse(text) : null; } catch { body = text; }
  if (!res.ok) {
    const msg = (body && body.error) || (typeof body === "string" ? body : res.statusText);
    throw new Error(msg || `HTTP ${res.status}`);
  }
  return body;
}

function escapeHtml(s) {
  return String(s || "").replace(/[&<>"']/g, c => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
  }[c]));
}

function setMsg(el, text, ok) {
  if (!el) return;
  el.textContent = text || "";
  el.className = "msg " + (ok === true ? "ok" : ok === false ? "err" : "");
}

function dsLabel(ds) {
  const s = String(ds || "").trim();
  if (!s || /^(none|notset|not set|n\/a|na|-)$/i.test(s)) return "datastore not set";
  return s;
}

function showTestModal(res) {
  const modal = $("#testModal");
  const summary = $("#testModalSummary");
  const list = $("#testModalChecks");
  if (!modal || !summary || !list) return;
  const ok = !!res?.ok;
  summary.textContent = ok
    ? "All required checks passed."
    : (res?.error || "One or more checks failed.");
  summary.className = "detail-meta " + (ok ? "msg ok" : "msg err");
  const checks = res?.checks || [];
  list.innerHTML = checks.length
    ? checks.map(ch => {
        const tone = ch.skip ? "skip" : (ch.ok ? "ok" : "err");
        const mark = ch.skip ? "—" : (ch.ok ? "✓" : "✗");
        return `<li class="check-row ${tone}"><span class="mark">${mark}</span><span class="name">${escapeHtml(ch.name)}</span><span class="detail">${escapeHtml(ch.detail || "")}</span></li>`;
      }).join("")
    : `<li class="check-row err"><span class="mark">✗</span><span class="name">Test</span><span class="detail">${escapeHtml(res?.error || "no result")}</span></li>`;
  modal.classList.remove("hidden");
}

function hideTestModal() {
  $("#testModal")?.classList.add("hidden");
}

document.addEventListener("click", (e) => {
  if (e.target.closest("[data-close-test-modal]")) hideTestModal();
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") hideTestModal();
});

let vcenters = [];
let mappings = [];
let globalDryRun = false;
/** @type {null | { mode: 'view'|'edit'|'create', id?: string, tab?: 'uuids'|'iso' }} */
let ui = null;
/** @type {'inventory'|'activity'} */
let pageView = "inventory";
let activityTimer = null;
const activityLastId = { inbound: 0, runtime: 0, outbound: 0 };
/** @type {'inbound'|'runtime'|'outbound'} */
let activityTab = "inbound";

function mappingsFor(vcId) {
  return mappings.filter(m => m.vcenterId === vcId);
}

function syncDryRunToggles() {
  const a = $("#globalDryRun");
  const b = $("#activityDryRun");
  if (a) a.checked = globalDryRun;
  if (b) b.checked = globalDryRun;
  document.body.classList.toggle("dry-run-on", globalDryRun);
}

async function setGlobalDryRun(on) {
  const res = await api("/api/v1/settings", {
    method: "PUT",
    body: JSON.stringify({ dryRun: !!on }),
  });
  globalDryRun = !!res.dryRun;
  syncDryRunToggles();
  await refreshStatus();
}

async function refreshStatus() {
  const st = await api("/api/v1/status");
  globalDryRun = !!st.dryRun;
  syncDryRunToggles();
  const dry = globalDryRun ? " · DRY-RUN" : "";
  $("#statusLine").textContent =
    `${st.version} · ${st.vcenters} vCenter · ${st.mappings} UUID${dry}`;
}

function setPageView(view) {
  pageView = view === "activity" ? "activity" : "inventory";
  $("#viewInventory")?.classList.toggle("hidden", pageView !== "inventory");
  $("#viewActivity")?.classList.toggle("hidden", pageView !== "activity");
  $("#navInventory")?.classList.toggle("active", pageView === "inventory");
  $("#navActivity")?.classList.toggle("active", pageView === "activity");
  if (pageView === "activity") startActivityPoll();
  else stopActivityPoll();
}

function stopActivityPoll() {
  if (activityTimer) {
    clearInterval(activityTimer);
    activityTimer = null;
  }
}

function setActivityTab(ch) {
  activityTab = ch === "runtime" || ch === "outbound" ? ch : "inbound";
  document.querySelectorAll("[data-activity-tab]").forEach(btn => {
    const on = btn.getAttribute("data-activity-tab") === activityTab;
    btn.classList.toggle("active", on);
    btn.setAttribute("aria-selected", on ? "true" : "false");
  });
  $("#paneInbound")?.classList.toggle("hidden", activityTab !== "inbound");
  $("#paneRuntime")?.classList.toggle("hidden", activityTab !== "runtime");
  $("#paneOutbound")?.classList.toggle("hidden", activityTab !== "outbound");
}

function startActivityPoll() {
  stopActivityPoll();
  setActivityTab(activityTab);
  refreshActivity(true);
  activityTimer = setInterval(() => refreshActivity(false), 2000);
}

function formatActivityTime(ts) {
  try {
    const d = new Date(ts);
    return d.toLocaleTimeString([], { hour12: false, hour: "2-digit", minute: "2-digit", second: "2-digit" });
  } catch {
    return "";
  }
}

function renderActivityChannel(el, events, replace) {
  if (!el) return;
  if (replace) el.innerHTML = "";
  if (!events.length && replace) {
    el.innerHTML = `<div class="activity-empty">No events yet.</div>`;
    return;
  }
  const html = events.map(ev => {
    const detail = ev.detail ? ` ${escapeHtml(JSON.stringify(ev.detail))}` : "";
    return `<div class="activity-line level-${escapeHtml(ev.level || "info")}${ev.dryRun ? " dry" : ""}">
      <span class="t">${escapeHtml(formatActivityTime(ev.ts))}</span>
      <span class="op">${escapeHtml(ev.op || "")}</span>
      <span class="m">${escapeHtml(ev.message || "")}${detail}</span>
    </div>`;
  }).join("");
  if (replace) el.innerHTML = html;
  else {
    el.insertAdjacentHTML("beforeend", html);
    el.scrollTop = el.scrollHeight;
  }
}

async function refreshActivity(full) {
  const channels = [
    ["inbound", "logInbound"],
    ["runtime", "logRuntime"],
    ["outbound", "logOutbound"],
  ];
  await Promise.all(channels.map(async ([ch, id]) => {
    const q = full
      ? `/api/v1/activity?channel=${ch}&limit=200`
      : `/api/v1/activity?channel=${ch}&after=${activityLastId[ch]}&limit=100`;
    try {
      const res = await api(q);
      const events = res.events || [];
      if (!events.length) {
        if (full) renderActivityChannel($("#" + id), [], true);
        return;
      }
      activityLastId[ch] = events[events.length - 1].id;
      renderActivityChannel($("#" + id), events, full);
    } catch (err) {
      const el = $("#" + id);
      if (el && full) el.innerHTML = `<div class="activity-empty err">${escapeHtml(err.message)}</div>`;
    }
  }));
}

function renderVCList() {
  const box = $("#vcList");
  if (!vcenters.length) {
    box.innerHTML = `<div class="vc-empty">No vCenters yet.<br/>Click <strong>Add</strong> to create one.</div>`;
    return;
  }
  box.innerHTML = vcenters.map(vc => {
    const n = mappingsFor(vc.id).length;
    const active = ui && (ui.id === vc.id || (ui.mode === "edit" && ui.id === vc.id));
    const dry = vc.dryRun || globalDryRun
      ? `<span class="pill-tag dry">dry-run</span>`
      : "";
    return `
      <button type="button" class="vc-item ${active ? "active" : ""}" data-select="${vc.id}">
        <div class="name">${escapeHtml(vc.name)} ${dry}</div>
        <p class="meta">${escapeHtml(vc.url)}</p>
        <p class="meta">${escapeHtml(vc.datacenter)} / ${escapeHtml(dsLabel(vc.datastore))}</p>
        <span class="count">${n} UUID${n === 1 ? "" : "s"}</span>
      </button>`;
  }).join("");
}

function emptyDetail() {
  $("#detailBody").innerHTML = `
    <div class="empty-state">
      <div class="ph-icon" aria-hidden="true"></div>
      <p class="empty-title">Select a vCenter</p>
      <p>Pick one on the left to see its UUIDs, edit connection settings, or bind a new system.</p>
    </div>`;
}

function cloneVCForm() {
  return $("#tplVCForm").content.firstElementChild.cloneNode(true);
}

function fillVCForm(form, vc) {
  form.id.value = vc?.id || "";
  form.name.value = vc?.name || "";
  form.url.value = vc?.url || "";
  form.username.value = vc?.username || "";
  form.password.value = "";
  form.datacenter.value = vc?.datacenter || "";
  form.datastore.value = (vc?.datastore && !/^(none|notset)$/i.test(vc.datastore)) ? vc.datastore : "";
  form.folder.value = vc?.folder || "";
  form.isoFolder.value = vc?.isoFolder || "rf2vc/isos";
  form.insecure.checked = !!vc?.insecure;
  form.notes.value = vc?.notes || "";
}

function formBody(form) {
  return {
    name: form.name.value.trim(),
    url: form.url.value.trim(),
    username: form.username.value.trim(),
    password: form.password.value,
    datacenter: form.datacenter.value.trim(),
    datastore: form.datastore.value.trim(),
    folder: form.folder.value.trim(),
    isoFolder: form.isoFolder.value.trim() || "rf2vc/isos",
    insecure: form.insecure.checked,
    notes: form.notes.value.trim(),
  };
}

function showCreateForm() {
  ui = { mode: "create" };
  renderVCList();
  const form = cloneVCForm();
  fillVCForm(form, null);
  $("#detailBody").innerHTML = `
    <div class="detail-head">
      <div>
        <p class="caps">New endpoint</p>
        <h2 class="detail-title">Add vCenter</h2>
        <p class="detail-meta">GOVC-shaped fields. Credentials persist on the PVC map.</p>
      </div>
    </div>`;
  $("#detailBody").appendChild(form);
  wireVCForm(form);
}

function showEditForm(vc) {
  ui = { mode: "edit", id: vc.id };
  renderVCList();
  const form = cloneVCForm();
  fillVCForm(form, vc);
  $("#detailBody").innerHTML = `
    <div class="detail-head">
      <div>
        <p class="caps">Edit endpoint</p>
        <h2 class="detail-title">${escapeHtml(vc.name)}</h2>
        <p class="detail-meta">Leave password blank to keep the stored secret.</p>
      </div>
    </div>`;
  $("#detailBody").appendChild(form);
  wireVCForm(form);
}

function lightHTML(tone, label) {
  const t = tone || "red";
  return `<span class="status-light" title="${escapeHtml(label || t)}"><span class="dot ${escapeHtml(t)}" aria-hidden="true"></span><span class="lbl">${escapeHtml(label || "")}</span></span>`;
}

function showVCView(vc) {
  const tab = (ui && ui.id === vc.id && ui.tab) || "uuids";
  ui = { mode: "view", id: vc.id, tab };
  renderVCList();
  const mapped = mappingsFor(vc.id);
  const notes = (vc.notes || "").trim();
  const notesDup = notes && (notes === vc.folder || notes === vc.url);

  $("#detailBody").innerHTML = `
    <div class="detail-head">
      <div class="detail-main">
        <p class="caps">vCenter</p>
        <h2 class="detail-title">${escapeHtml(vc.name)}</h2>
        <p class="detail-url">${escapeHtml(vc.url)}</p>
        <dl class="field-grid">
          <div class="field-row"><dt>Datacenter</dt><dd>${escapeHtml(vc.datacenter || "—")}</dd></div>
          <div class="field-row"><dt>Datastore</dt><dd>${escapeHtml(dsLabel(vc.datastore))}</dd></div>
          <div class="field-row"><dt>Username</dt><dd>${escapeHtml(vc.username || "—")}${vc.insecure ? ' <span class="pill-tag">insecure</span>' : ""}</dd></div>
          <div class="field-row"><dt>VM folder</dt><dd>${vc.folder ? `${escapeHtml(vc.folder)} <span class="pill-tag">recursive</span>` : "<em>not set</em>"}</dd></div>
          <div class="field-row"><dt>ISO folder</dt><dd>${escapeHtml(vc.isoFolder || "rf2vc/isos")}</dd></div>
          ${notes && !notesDup ? `<div class="field-row"><dt>Notes</dt><dd>${escapeHtml(notes)}</dd></div>` : ""}
        </dl>
        <div class="health-block">
          <div class="health-row" id="healthRow">
            ${lightHTML("yellow", "Connection…")}
            ${lightHTML("yellow", "ISO cache…")}
          </div>
          <ul class="health-checks" id="healthChecks">
            <li class="health-check skip"><span class="mark">…</span><span class="name">Probing…</span></li>
          </ul>
          <p class="msg" id="vcTestMsg"></p>
        </div>
      </div>
      <div class="detail-actions">
        <label class="dry-toggle sm" title="Fake outbound mutations for this vCenter only">
          <input type="checkbox" data-vc-dry="${vc.id}" ${vc.dryRun || globalDryRun ? "checked" : ""} ${globalDryRun ? "disabled" : ""} />
          <span>Dry-run</span>
        </label>
        <button type="button" class="pill ghost sm" data-test-vc="${vc.id}">Test</button>
        <button type="button" class="pill ghost sm" data-refresh-health="${vc.id}">Refresh</button>
        <button type="button" class="pill ghost sm" data-edit="${vc.id}">Edit</button>
        <button type="button" class="pill danger sm" data-delete="${vc.id}">Delete</button>
      </div>
    </div>

    <div class="tabs" role="tablist">
      <button type="button" class="tab ${tab === "uuids" ? "active" : ""}" data-tab="uuids" role="tab">UUIDs <span id="uuidTabCount">(${mapped.length})</span></button>
      <button type="button" class="tab ${tab === "iso" ? "active" : ""}" data-tab="iso" role="tab">ISO cache</button>
    </div>

    <div class="tab-panel ${tab === "uuids" ? "" : "hidden"}" id="tabUuids">
      <p class="detail-meta" id="folderVmSummary">Loading folder VMs…</p>
      <div class="uuid-list" id="uuidList"></div>
      <form class="bind-form" id="bindForm">
        <label>BIOS UUID <input name="uuid" required placeholder="4235a1b2-… (add even if not listed)" /></label>
        <label>Name <input name="name" placeholder="MO-OCLAB-CP01" /></label>
        <label>Notes <input name="notes" /></label>
        <button type="submit" class="pill primary">Add UUID</button>
      </form>
      <p class="msg" id="bindMsg"></p>
    </div>

    <div class="tab-panel ${tab === "iso" ? "" : "hidden"}" id="tabIso">
      <div class="section-head">
        <h3>Staged ISOs</h3>
        <button type="button" class="pill ghost sm" data-refresh-iso="${vc.id}">Refresh</button>
      </div>
      <p class="detail-meta" id="isoSummary">Loading…</p>
      <div class="iso-list" id="isoList"></div>
      <p class="msg" id="isoMsg"></p>
    </div>
  `;

  loadHealth(vc.id);
  if (tab === "iso") loadISOStatus(vc.id);
  else loadUUIDTab(vc);

  $("#bindForm").addEventListener("submit", async (e) => {
    e.preventDefault();
    const f = e.target;
    try {
      await api("/api/v1/mappings", {
        method: "POST",
        body: JSON.stringify({
          uuid: f.uuid.value.trim(),
          vcenterId: vc.id,
          name: f.name.value.trim(),
          notes: f.notes.value.trim(),
        }),
      });
      setMsg($("#bindMsg"), "Bound", true);
      await reload(vc.id);
    } catch (err) {
      setMsg($("#bindMsg"), err.message, false);
    }
  });
}

/** Merge folder inventory + persisted mappings for the UUIDs tab. */
function mergeUUIDRows(mapped, discovered) {
  const byUUID = new Map();
  for (const vm of discovered || []) {
    byUUID.set(vm.uuid, {
      uuid: vm.uuid,
      name: vm.name || "System",
      notes: "",
      path: vm.path || "",
      powerState: vm.powerState || "",
      light: vm.light || "yellow",
      bound: false,
      inFolder: true,
    });
  }
  for (const m of mapped || []) {
    const existing = byUUID.get(m.uuid);
    if (existing) {
      existing.bound = true;
      existing.notes = m.notes || "";
      if (m.name) existing.name = m.name;
    } else {
      byUUID.set(m.uuid, {
        uuid: m.uuid,
        name: m.name || "System",
        notes: m.notes || "",
        path: "",
        powerState: "",
        light: "yellow",
        bound: true,
        inFolder: false,
      });
    }
  }
  return Array.from(byUUID.values()).sort((a, b) =>
    (a.name || a.uuid).localeCompare(b.name || b.uuid, undefined, { sensitivity: "base" })
  );
}

function renderUUIDRows(vc, rows) {
  const list = $("#uuidList");
  if (!list) return;
  if (!rows.length) {
    list.innerHTML = `<div class="vc-empty">No VMs in folder and no UUIDs bound yet.${vc.folder ? "" : "<br/>Set Folder (GOVC_FOLDER) or Add UUID below."}</div>`;
    return;
  }
  list.innerHTML = rows.map(m => {
    const tags = [];
    if (m.inFolder) tags.push(`<span class="pill-tag">folder</span>`);
    if (m.bound) tags.push(`<span class="pill-tag bound">bound</span>`);
    else tags.push(`<span class="pill-tag unbound">not bound</span>`);
    const shortUuid = m.uuid.length > 13 ? `${m.uuid.slice(0, 8)}…` : m.uuid;
    const actions = m.bound
      ? `
        <button type="button" class="pill danger sm" data-unmap="${escapeHtml(m.uuid)}" title="Remove UUID mapping (does not delete the VM)">Unbind</button>
        <button type="button" class="pill ghost sm" data-uuid-status="${escapeHtml(m.uuid)}">Status</button>
        <button type="button" class="pill ghost sm" data-uuid-on="${escapeHtml(m.uuid)}" disabled>Power on</button>
        <button type="button" class="pill ghost sm" data-uuid-off="${escapeHtml(m.uuid)}" disabled>Power off</button>
        <button type="button" class="pill ghost sm" data-uuid-iso="${escapeHtml(m.uuid)}">ISO map</button>`
      : `
        <button type="button" class="pill primary sm" data-bind-folder="${escapeHtml(m.uuid)}" data-bind-name="${escapeHtml(m.name)}" title="Map this BIOS UUID so ACM/BMH Redfish calls control this VM">Bind</button>`;
    return `
    <div class="uuid-row" data-uuid-row="${escapeHtml(m.uuid)}">
      <button type="button" class="uuid-summary" data-toggle-uuid="${escapeHtml(m.uuid)}" aria-expanded="false">
        <span class="chev" aria-hidden="true"></span>
        <span class="status-light" data-light="${escapeHtml(m.uuid)}"><span class="dot ${escapeHtml(m.light || "yellow")}"></span></span>
        <span class="title">${escapeHtml(m.name || "System")}</span>
        <span class="uuid-tags">${tags.join("")}</span>
        <span class="uuid-short" title="${escapeHtml(m.uuid)}">${escapeHtml(shortUuid)}</span>
      </button>
      <div class="uuid-actions">${actions}</div>
      <p class="msg uuid-row-msg" data-row-msg="${escapeHtml(m.uuid)}"></p>
      <div class="uuid-details hidden" data-uuid-details="${escapeHtml(m.uuid)}">
        <div class="detail-grid">
          <span class="k">BIOS UUID</span><code class="v">${escapeHtml(m.uuid)}</code>
          <span class="k">Redfish</span><code class="v">/redfish/v1/Systems/${escapeHtml(m.uuid)}</code>
          <span class="k">Path</span><span class="v" data-vm-path="${escapeHtml(m.uuid)}">${m.path ? escapeHtml(m.path) : "—"}</span>
          ${m.notes ? `<span class="k">Notes</span><span class="v">${escapeHtml(m.notes)}</span>` : ""}
          <span class="k">CDROM</span><span class="v" data-cdrom="${escapeHtml(m.uuid)}">—</span>
        </div>
      </div>
    </div>`;
  }).join("");
}

async function loadUUIDTab(vc) {
  const summary = $("#folderVmSummary");
  const mapped = mappingsFor(vc.id);
  let discovered = [];
  let folderMsg = "";
  if (!vc.folder) {
    folderMsg = "Folder not set — showing bound UUIDs only. Set GOVC_FOLDER to discover VMs under that path (recursive).";
  } else {
    if (summary) summary.textContent = `Scanning ${vc.folder} (recursive)… — watch Activity → Runtime or oc logs -f`;
    try {
      const res = await api(`/api/v1/vcenters/${vc.id}/vms`);
      discovered = res.vms || [];
      folderMsg = res.error
        ? `Folder scan failed: ${res.error}`
        : `Folder · ${discovered.length} VM(s) under ${vc.folder} (includes subfolders) · ${mapped.length} bound`;
      if (!res.error && discovered.length === 0) {
        folderMsg += " — see Activity / Runtime for candidate paths tried";
      }
    } catch (err) {
      folderMsg = `Folder scan failed: ${err.message}`;
    }
  }
  if (summary) summary.textContent = folderMsg;
  const rows = mergeUUIDRows(mapped, discovered);
  const countEl = $("#uuidTabCount");
  if (countEl) countEl.textContent = `(${rows.length})`;
  renderUUIDRows(vc, rows);
  await Promise.all(rows.filter(r => r.bound).map(r => loadUUIDStatus(r.uuid)));
}

function applyUUIDStatus(uuid, st) {
  const light = document.querySelector(`[data-light="${CSS.escape(uuid)}"]`);
  if (light) {
    const tone = st.light || "red";
    light.innerHTML = `<span class="dot ${escapeHtml(tone)}" title="${escapeHtml(st.powerState || st.error || tone)}"></span>`;
  }
  const pathEl = document.querySelector(`[data-vm-path="${CSS.escape(uuid)}"]`);
  if (pathEl) {
    if (st.path) pathEl.textContent = st.path;
    else if (st.found && !pathEl.textContent) pathEl.textContent = "—";
  }
  const cdEl = document.querySelector(`[data-cdrom="${CSS.escape(uuid)}"]`);
  if (cdEl) {
    if (st.cdromIso) cdEl.textContent = st.cdromIso;
    else if (st.found) cdEl.textContent = "(none / empty)";
    else if (st.error) cdEl.textContent = "—";
  }
  const onBtn = document.querySelector(`[data-uuid-on="${CSS.escape(uuid)}"]`);
  const offBtn = document.querySelector(`[data-uuid-off="${CSS.escape(uuid)}"]`);
  if (onBtn) onBtn.disabled = !(st.found && st.powerState === "Off");
  if (offBtn) offBtn.disabled = !(st.found && st.powerState === "On");
}

function rowMsg(uuid) {
  return document.querySelector(`[data-row-msg="${CSS.escape(uuid)}"]`);
}

function expandUUIDRow(uuid) {
  const row = document.querySelector(`[data-uuid-row="${CSS.escape(uuid)}"]`);
  const details = document.querySelector(`[data-uuid-details="${CSS.escape(uuid)}"]`);
  const btn = row?.querySelector("[data-toggle-uuid]");
  if (!row || !details || !btn) return;
  details.classList.remove("hidden");
  row.classList.add("open");
  btn.setAttribute("aria-expanded", "true");
}

async function loadUUIDStatus(uuid) {
  try {
    const st = await api(`/api/v1/mappings/${encodeURIComponent(uuid)}/status`);
    applyUUIDStatus(uuid, st);
    return st;
  } catch (err) {
    applyUUIDStatus(uuid, { light: "red", found: false, error: err.message });
    return { found: false, error: err.message, light: "red" };
  }
}

function healthCheckHTML(ok, skip, name, detail) {
  const tone = skip ? "skip" : (ok ? "ok" : "err");
  const mark = skip ? "—" : (ok ? "✓" : "✗");
  return `<li class="health-check ${tone}"><span class="mark">${mark}</span><span class="name">${escapeHtml(name)}</span><span class="detail">${escapeHtml(detail || "")}</span></li>`;
}

async function loadHealth(vcId) {
  const row = $("#healthRow");
  const checks = $("#healthChecks");
  if (!row) return;
  try {
    const h = await api(`/api/v1/vcenters/${vcId}/health`);
    row.innerHTML =
      lightHTML(h.connection, "Connection") +
      lightHTML(h.isoCache, "ISO cache");
    if (checks) {
      const connOk = h.connection === "green";
      const connWarn = h.connection === "yellow";
      const isoOk = h.isoCache === "green";
      const isoWarn = h.isoCache === "yellow";
      const folderSet = !!(h.folderPath || h.folderOk);
      checks.innerHTML =
        healthCheckHTML(connOk || connWarn, false, "Login / datacenter", h.connectionDetail || h.connection) +
        healthCheckHTML(!!h.folderOk, !folderSet && !h.folderPath, "VM folder",
          h.folderPath ? (h.folderOk ? h.folderPath : (h.connectionDetail || "folder issue")) : "not set") +
        healthCheckHTML(isoOk || isoWarn, false, "ISO / datastore", h.isoCacheDetail || h.isoCache);
    }
  } catch (err) {
    row.innerHTML = lightHTML("red", "Connection") + lightHTML("red", "ISO cache");
    if (checks) {
      checks.innerHTML = healthCheckHTML(false, false, "Health", err.message);
    }
  }
}

function formatBytes(n) {
  if (!n || n <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v < 10 && i > 0 ? v.toFixed(1) : Math.round(v)} ${units[i]}`;
}

async function loadISOStatus(vcId) {
  const summary = $("#isoSummary");
  const list = $("#isoList");
  const msg = $("#isoMsg");
  if (!summary || !list) return;
  summary.textContent = "Loading…";
  list.innerHTML = "";
  setMsg(msg, "", null);
  try {
    const st = await api(`/api/v1/vcenters/${vcId}/iso-status`);
    const reach = st.reachable ? "reachable" : "unreachable";
    summary.textContent =
      `${st.datastore || "?"} · ${st.isoFolder || "?"} · ${reach}` +
      ` · ${st.datastoreCount || 0} on datastore · ${st.localCacheCount || 0} local`;
    if (st.error) setMsg(msg, st.error, false);
    const files = st.files || [];
    if (!files.length) {
      list.innerHTML = `<div class="vc-empty">No hashed ISOs staged yet. InsertMedia will download once, then reuse the datastore file.</div>`;
      return;
    }
    list.innerHTML = files.map(f => {
      const flags = [
        f.onDatastore ? `DS ${formatBytes(f.datastoreSize)}` : "not on DS",
        f.localCached ? `local ${formatBytes(f.localSize)}` : null,
      ].filter(Boolean).join(" · ");
      return `
        <div class="iso-row">
          <div class="title">${escapeHtml(f.name)}</div>
          <div class="meta">${escapeHtml(f.datastorePath || "")}</div>
          <div class="flags">${escapeHtml(flags)}</div>
        </div>`;
    }).join("");
  } catch (err) {
    summary.textContent = "ISO status unavailable";
    setMsg(msg, err.message, false);
  }
}

function wireVCForm(form) {
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const body = formBody(form);
    const msg = form.querySelector('[data-msg="vc"]');
    try {
      let out;
      if (form.id.value) {
        out = await api(`/api/v1/vcenters/${form.id.value}`, {
          method: "PUT",
          body: JSON.stringify(body),
        });
      } else {
        if (!body.password) throw new Error("password required for new vCenter");
        out = await api("/api/v1/vcenters", {
          method: "POST",
          body: JSON.stringify(body),
        });
      }
      setMsg(msg, "Saved", true);
      await reload(out.id);
    } catch (err) {
      setMsg(msg, err.message, false);
    }
  });

  form.querySelector('[data-action="cancel"]').addEventListener("click", () => {
    if (form.id.value) {
      const vc = vcenters.find(v => v.id === form.id.value);
      if (vc) showVCView(vc);
      else { ui = null; emptyDetail(); renderVCList(); }
    } else {
      ui = null;
      emptyDetail();
      renderVCList();
    }
  });

  form.querySelector('[data-action="test"]').addEventListener("click", async () => {
    const msg = form.querySelector('[data-msg="vc"]');
    const body = formBody(form);
    setMsg(msg, "Testing…", null);
    try {
      let res;
      if (form.id.value) {
        res = await api(`/api/v1/vcenters/${form.id.value}/test`, {
          method: "POST",
          body: JSON.stringify(body),
        });
      } else {
        if (!body.password) throw new Error("password required to test a new vCenter");
        res = await api("/api/v1/vcenters/test", {
          method: "POST",
          body: JSON.stringify(body),
        });
      }
      showTestModal(res);
      setMsg(msg, res.ok ? "Connection OK — see checklist" : (res.error || "failed — see checklist"), !!res.ok);
    } catch (err) {
      showTestModal({ ok: false, error: err.message, checks: [] });
      setMsg(msg, err.message, false);
    }
  });
}

async function reload(selectId) {
  [vcenters, mappings] = await Promise.all([
    api("/api/v1/vcenters"),
    api("/api/v1/mappings"),
  ]);
  await refreshStatus();
  const id = selectId || (ui && ui.id);
  if (id) {
    const vc = vcenters.find(v => v.id === id);
    if (vc) showVCView(vc);
    else {
      ui = null;
      renderVCList();
      emptyDetail();
    }
  } else {
    renderVCList();
    if (!ui || ui.mode !== "create") emptyDetail();
  }
}

$("#btnAddVC").addEventListener("click", showCreateForm);
$("#btnAddVC2").addEventListener("click", showCreateForm);

document.querySelectorAll("[data-view]").forEach(btn => {
  btn.addEventListener("click", () => setPageView(btn.getAttribute("data-view")));
});

document.querySelectorAll("[data-activity-tab]").forEach(btn => {
  btn.addEventListener("click", () => {
    setActivityTab(btn.getAttribute("data-activity-tab"));
    // Ensure the newly visible pane has content
    refreshActivity(false);
  });
});

async function onDryRunToggle(e) {
  const on = e.target.checked;
  try {
    await setGlobalDryRun(on);
    if (ui?.id) {
      const vc = vcenters.find(v => v.id === ui.id);
      if (vc && ui.mode === "view") showVCView(vc);
      else renderVCList();
    } else renderVCList();
  } catch (err) {
    e.target.checked = !on;
    alert(err.message);
  }
}
$("#globalDryRun")?.addEventListener("change", onDryRunToggle);
$("#activityDryRun")?.addEventListener("change", onDryRunToggle);

$("#detailPane").addEventListener("change", async (e) => {
  const input = e.target.closest("[data-vc-dry]");
  if (!input || !input.matches("input[type=checkbox]")) return;
  const id = input.getAttribute("data-vc-dry");
  const on = input.checked;
  try {
    const out = await api(`/api/v1/vcenters/${id}/dry-run`, {
      method: "PUT",
      body: JSON.stringify({ dryRun: on }),
    });
    const idx = vcenters.findIndex(v => v.id === id);
    if (idx >= 0) vcenters[idx] = { ...vcenters[idx], dryRun: out.dryRun };
    renderVCList();
  } catch (err) {
    input.checked = !on;
    alert(err.message);
  }
});

$("#vcList").addEventListener("click", (e) => {
  const id = e.target.closest("[data-select]")?.getAttribute("data-select");
  if (!id) return;
  setPageView("inventory");
  const vc = vcenters.find(v => v.id === id);
  if (vc) showVCView(vc);
});

$("#detailPane").addEventListener("click", async (e) => {
  if (e.target.closest("[data-vc-dry]")) return;

  const tab = e.target.getAttribute("data-tab");
  if (tab && ui?.id) {
    ui.tab = tab;
    const vc = vcenters.find(v => v.id === ui.id);
    if (vc) showVCView(vc);
    return;
  }

  const toggleUuid = e.target.closest("[data-toggle-uuid]")?.getAttribute("data-toggle-uuid");
  if (toggleUuid) {
    const row = document.querySelector(`[data-uuid-row="${CSS.escape(toggleUuid)}"]`);
    const details = document.querySelector(`[data-uuid-details="${CSS.escape(toggleUuid)}"]`);
    const btn = e.target.closest("[data-toggle-uuid]");
    if (row && details && btn) {
      const open = details.classList.toggle("hidden") === false;
      row.classList.toggle("open", open);
      btn.setAttribute("aria-expanded", open ? "true" : "false");
    }
    return;
  }

  const edit = e.target.getAttribute("data-edit");
  const del = e.target.getAttribute("data-delete");
  const unmap = e.target.getAttribute("data-unmap");
  const refreshIso = e.target.getAttribute("data-refresh-iso");
  const refreshHealth = e.target.getAttribute("data-refresh-health");
  const testVc = e.target.getAttribute("data-test-vc");
  const uuidStatus = e.target.getAttribute("data-uuid-status");
  const uuidOn = e.target.getAttribute("data-uuid-on");
  const uuidOff = e.target.getAttribute("data-uuid-off");
  const uuidIso = e.target.getAttribute("data-uuid-iso");
  const bindFolder = e.target.getAttribute("data-bind-folder");
  const bindName = e.target.getAttribute("data-bind-name");

  if (edit) {
    const vc = vcenters.find(v => v.id === edit);
    if (vc) showEditForm(vc);
  }
  if (del) {
    if (!confirm("Delete this vCenter and all UUID mappings under it?")) return;
    await api(`/api/v1/vcenters/${del}`, { method: "DELETE" });
    ui = null;
    await reload();
  }
  if (unmap) {
    if (!confirm("Unbind this UUID? The VM stays in vSphere; ACM/BMH will no longer reach it via rf2vc.")) return;
    await api(`/api/v1/mappings/${encodeURIComponent(unmap)}`, { method: "DELETE" });
    await reload(ui?.id);
  }
  if (bindFolder && ui?.id) {
    const msg = rowMsg(bindFolder);
    setMsg(msg, "Binding…", null);
    try {
      await api("/api/v1/mappings", {
        method: "POST",
        body: JSON.stringify({
          uuid: bindFolder,
          vcenterId: ui.id,
          name: bindName || "",
        }),
      });
      setMsg(msg, "Bound — UUID mapped for Redfish", true);
      await reload(ui.id);
      // After reload, leave a brief success note on the bound row if still present.
      const after = rowMsg(bindFolder);
      if (after) setMsg(after, "Bound", true);
    } catch (err) {
      expandUUIDRow(bindFolder);
      setMsg(msg, err.message, false);
    }
  }
  if (refreshIso) await loadISOStatus(refreshIso);
  if (refreshHealth) {
    await loadHealth(refreshHealth);
    const vc = vcenters.find(v => v.id === refreshHealth);
    if (vc) await loadUUIDTab(vc);
  }
  if (testVc) {
    const msg = $("#vcTestMsg");
    setMsg(msg, "Testing…", null);
    try {
      const res = await api(`/api/v1/vcenters/${testVc}/test`, { method: "POST", body: "{}" });
      showTestModal(res);
      setMsg(msg, res.ok ? "Connection OK — see checklist" : (res.error || "failed — see checklist"), !!res.ok);
      await loadHealth(testVc);
      const vc = vcenters.find(v => v.id === testVc);
      if (vc) await loadUUIDTab(vc);
    } catch (err) {
      showTestModal({ ok: false, error: err.message, checks: [] });
      setMsg(msg, err.message, false);
    }
  }
  if (uuidStatus) {
    expandUUIDRow(uuidStatus);
    const msg = rowMsg(uuidStatus);
    setMsg(msg, "Probing vSphere…", null);
    const st = await loadUUIDStatus(uuidStatus);
    if (!st) {
      setMsg(msg, "status unavailable", false);
    } else if (st.found && st.powerState) {
      setMsg(msg, `${st.powerState} · ${st.name || "VM"}${st.error ? " · " + st.error : ""}`, !st.error || st.light !== "red");
    } else if (st.found) {
      setMsg(msg, st.error || "found (limited props)", st.error ? false : true);
    } else {
      setMsg(msg, st.error || "not found", false);
    }
  }
  if (uuidOn) {
    try {
      const res = await api(`/api/v1/mappings/${encodeURIComponent(uuidOn)}/power`, {
        method: "POST",
        body: JSON.stringify({ resetType: "On" }),
      });
      if (res.status) applyUUIDStatus(uuidOn, res.status);
      setMsg(rowMsg(uuidOn), res.dryRun ? "dry-run: power on not sent" : "power on requested", true);
    } catch (err) {
      setMsg(rowMsg(uuidOn), err.message, false);
    }
  }
  if (uuidOff) {
    try {
      const res = await api(`/api/v1/mappings/${encodeURIComponent(uuidOff)}/power`, {
        method: "POST",
        body: JSON.stringify({ resetType: "ForceOff" }),
      });
      if (res.status) applyUUIDStatus(uuidOff, res.status);
      setMsg(rowMsg(uuidOff), res.dryRun ? "dry-run: power off not sent" : "power off requested", true);
    } catch (err) {
      setMsg(rowMsg(uuidOff), err.message, false);
    }
  }
  if (uuidIso) {
    expandUUIDRow(uuidIso);
    const st = await loadUUIDStatus(uuidIso);
    const msg = rowMsg(uuidIso);
    if (st?.cdromIso) setMsg(msg, `CDROM · ${st.cdromIso}`, true);
    else if (st?.found) setMsg(msg, st.error || "CDROM empty / no ISO backing", st.error ? false : true);
    else setMsg(msg, st?.error || "unavailable", false);
  }
});

reload().catch(err => {
  $("#statusLine").textContent = "API error: " + err.message;
});
