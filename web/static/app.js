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
/** @type {null | { mode: 'view'|'edit'|'create', id?: string, tab?: 'uuids'|'iso' }} */
let ui = null;

function mappingsFor(vcId) {
  return mappings.filter(m => m.vcenterId === vcId);
}

async function refreshStatus() {
  const st = await api("/api/v1/status");
  $("#statusLine").textContent =
    `${st.version} · ${st.vcenters} vCenter · ${st.mappings} UUID`;
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
    return `
      <button type="button" class="vc-item ${active ? "active" : ""}" data-select="${vc.id}">
        <div class="name">${escapeHtml(vc.name)}</div>
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

  $("#detailBody").innerHTML = `
    <div class="detail-head">
      <div>
        <p class="caps">vCenter</p>
        <h2 class="detail-title">${escapeHtml(vc.name)}</h2>
        <p class="detail-meta">${escapeHtml(vc.url)}</p>
        <p class="detail-meta">${escapeHtml(vc.datacenter)} / ${escapeHtml(dsLabel(vc.datastore))} · ${escapeHtml(vc.username)}${vc.insecure ? " · insecure" : ""}</p>
        ${vc.folder ? `<p class="detail-meta">Folder · ${escapeHtml(vc.folder)} <span class="pill-tag">recursive</span></p>` : `<p class="detail-meta">Folder · <em>not set</em> (set GOVC_FOLDER to discover VMs)</p>`}
        <p class="detail-meta">ISO folder · ${escapeHtml(vc.isoFolder || "rf2vc/isos")}</p>
        ${vc.notes ? `<p class="detail-meta">${escapeHtml(vc.notes)}</p>` : ""}
        <div class="health-row" id="healthRow">
          ${lightHTML("yellow", "Connection…")}
          ${lightHTML("yellow", "ISO cache…")}
        </div>
        <p class="detail-meta" id="healthDetail"></p>
        <p class="msg" id="vcTestMsg"></p>
      </div>
      <div class="detail-actions">
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
    const actions = m.bound
      ? `
        <button type="button" class="pill ghost sm" data-uuid-status="${escapeHtml(m.uuid)}">Status</button>
        <button type="button" class="pill ghost sm" data-uuid-on="${escapeHtml(m.uuid)}" disabled>Power on</button>
        <button type="button" class="pill ghost sm" data-uuid-off="${escapeHtml(m.uuid)}" disabled>Power off</button>
        <button type="button" class="pill ghost sm" data-uuid-iso="${escapeHtml(m.uuid)}">ISO map</button>
        <button type="button" class="pill danger sm" data-unmap="${escapeHtml(m.uuid)}">Remove</button>`
      : `
        <button type="button" class="pill primary sm" data-bind-folder="${escapeHtml(m.uuid)}" data-bind-name="${escapeHtml(m.name)}">Bind</button>`;
    return `
    <div class="uuid-row" data-uuid-row="${escapeHtml(m.uuid)}">
      <div class="uuid-main">
        <div class="uuid-title-row">
          <span class="status-light" data-light="${escapeHtml(m.uuid)}"><span class="dot ${escapeHtml(m.light || "yellow")}"></span></span>
          <div class="title">${escapeHtml(m.name || "System")}</div>
          <div class="uuid-tags">${tags.join(" ")}</div>
        </div>
        <div class="meta">${escapeHtml(m.uuid)}</div>
        ${m.notes ? `<div class="meta">${escapeHtml(m.notes)}</div>` : ""}
        <div class="path">/redfish/v1/Systems/${escapeHtml(m.uuid)}</div>
        <div class="meta" data-vm-path="${escapeHtml(m.uuid)}">${m.path ? `Path · ${escapeHtml(m.path)}` : ""}</div>
        <div class="meta" data-cdrom="${escapeHtml(m.uuid)}"></div>
        <p class="msg" data-row-msg="${escapeHtml(m.uuid)}"></p>
      </div>
      <div class="uuid-actions">${actions}</div>
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
    if (summary) summary.textContent = `Scanning ${vc.folder} (recursive)…`;
    try {
      const res = await api(`/api/v1/vcenters/${vc.id}/vms`);
      discovered = res.vms || [];
      folderMsg = res.error
        ? `Folder scan failed: ${res.error}`
        : `Folder · ${discovered.length} VM(s) under ${vc.folder} (includes subfolders) · ${mapped.length} bound`;
    } catch (err) {
      folderMsg = `Folder scan failed: ${err.message}`;
    }
  }
  if (summary) summary.textContent = folderMsg;
  const rows = mergeUUIDRows(mapped, discovered);
  const countEl = $("#uuidTabCount");
  if (countEl) countEl.textContent = `(${rows.length})`;
  renderUUIDRows(vc, rows);
  // Live status for bound rows; discovered-only already have light from inventory.
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
    pathEl.textContent = st.path ? `Path · ${st.path}` : (st.found ? "" : (st.error || "not found"));
  }
  const cdEl = document.querySelector(`[data-cdrom="${CSS.escape(uuid)}"]`);
  if (cdEl && st.cdromIso !== undefined) {
    cdEl.textContent = st.cdromIso ? `CDROM · ${st.cdromIso}` : (st.found ? "CDROM · (none / empty)" : "");
  }
  const onBtn = document.querySelector(`[data-uuid-on="${CSS.escape(uuid)}"]`);
  const offBtn = document.querySelector(`[data-uuid-off="${CSS.escape(uuid)}"]`);
  if (onBtn) onBtn.disabled = !(st.found && st.powerState === "Off");
  if (offBtn) offBtn.disabled = !(st.found && st.powerState === "On");
  const msg = document.querySelector(`[data-row-msg="${CSS.escape(uuid)}"]`);
  if (msg && st.error && st.light === "yellow") setMsg(msg, st.error, false);
  else if (msg) setMsg(msg, "", null);
}

async function loadUUIDStatus(uuid) {
  try {
    const st = await api(`/api/v1/mappings/${encodeURIComponent(uuid)}/status`);
    applyUUIDStatus(uuid, st);
    return st;
  } catch (err) {
    applyUUIDStatus(uuid, { light: "red", found: false, error: err.message });
    return null;
  }
}

async function loadAllUUIDStatus(rows) {
  await Promise.all((rows || []).map(m => loadUUIDStatus(m.uuid)));
}

async function loadHealth(vcId) {
  const row = $("#healthRow");
  const detail = $("#healthDetail");
  if (!row) return;
  try {
    const h = await api(`/api/v1/vcenters/${vcId}/health`);
    row.innerHTML =
      lightHTML(h.connection, "Connection") +
      lightHTML(h.isoCache, "ISO cache");
    if (detail) {
      detail.textContent = [h.connectionDetail, h.isoCacheDetail].filter(Boolean).join(" · ");
    }
  } catch (err) {
    row.innerHTML = lightHTML("red", "Connection") + lightHTML("red", "ISO cache");
    if (detail) detail.textContent = err.message;
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

$("#vcList").addEventListener("click", (e) => {
  const id = e.target.closest("[data-select]")?.getAttribute("data-select");
  if (!id) return;
  const vc = vcenters.find(v => v.id === id);
  if (vc) showVCView(vc);
});

$("#detailPane").addEventListener("click", async (e) => {
  const tab = e.target.getAttribute("data-tab");
  if (tab && ui?.id) {
    ui.tab = tab;
    const vc = vcenters.find(v => v.id === ui.id);
    if (vc) showVCView(vc);
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
    await api(`/api/v1/mappings/${encodeURIComponent(unmap)}`, { method: "DELETE" });
    await reload(ui?.id);
  }
  if (bindFolder && ui?.id) {
    try {
      await api("/api/v1/mappings", {
        method: "POST",
        body: JSON.stringify({
          uuid: bindFolder,
          vcenterId: ui.id,
          name: bindName || "",
        }),
      });
      await reload(ui.id);
    } catch (err) {
      setMsg(document.querySelector(`[data-row-msg="${CSS.escape(bindFolder)}"]`), err.message, false);
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
    const st = await loadUUIDStatus(uuidStatus);
    const msg = document.querySelector(`[data-row-msg="${CSS.escape(uuidStatus)}"]`);
    if (st?.found) setMsg(msg, `${st.powerState || "?"} · ${st.name || ""}`, true);
  }
  if (uuidOn) {
    try {
      const res = await api(`/api/v1/mappings/${encodeURIComponent(uuidOn)}/power`, {
        method: "POST",
        body: JSON.stringify({ resetType: "On" }),
      });
      if (res.status) applyUUIDStatus(uuidOn, res.status);
    } catch (err) {
      setMsg(document.querySelector(`[data-row-msg="${CSS.escape(uuidOn)}"]`), err.message, false);
    }
  }
  if (uuidOff) {
    try {
      const res = await api(`/api/v1/mappings/${encodeURIComponent(uuidOff)}/power`, {
        method: "POST",
        body: JSON.stringify({ resetType: "ForceOff" }),
      });
      if (res.status) applyUUIDStatus(uuidOff, res.status);
    } catch (err) {
      setMsg(document.querySelector(`[data-row-msg="${CSS.escape(uuidOff)}"]`), err.message, false);
    }
  }
  if (uuidIso) {
    const st = await loadUUIDStatus(uuidIso);
    const msg = document.querySelector(`[data-row-msg="${CSS.escape(uuidIso)}"]`);
    if (st?.cdromIso) setMsg(msg, `CDROM · ${st.cdromIso}`, true);
    else if (st?.found) setMsg(msg, "CDROM empty / passthrough", true);
    else setMsg(msg, st?.error || "unavailable", false);
  }
});

reload().catch(err => {
  $("#statusLine").textContent = "API error: " + err.message;
});
