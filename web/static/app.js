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

let vcenters = [];
let mappings = [];
/** @type {null | { mode: 'view'|'edit'|'create', id?: string }} */
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
        <p class="meta">${escapeHtml(vc.datacenter)} / ${escapeHtml(vc.datastore)}</p>
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
  form.datastore.value = vc?.datastore || "";
  form.isoFolder.value = vc?.isoFolder || "rf2vc/isos";
  form.insecure.checked = !!vc?.insecure;
  form.notes.value = vc?.notes || "";
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

function showVCView(vc) {
  ui = { mode: "view", id: vc.id };
  renderVCList();
  const rows = mappingsFor(vc.id);
  const uuidBlock = rows.length
    ? rows.map(m => `
        <div class="uuid-row">
          <div>
            <div class="title">${escapeHtml(m.name || "System")}</div>
            <div class="meta">${escapeHtml(m.uuid)}</div>
            ${m.notes ? `<div class="meta">${escapeHtml(m.notes)}</div>` : ""}
            <div class="path">/redfish/v1/Systems/${escapeHtml(m.uuid)}</div>
          </div>
          <button type="button" class="pill danger sm" data-unmap="${escapeHtml(m.uuid)}">Remove</button>
        </div>`).join("")
    : `<div class="vc-empty">No UUIDs bound to this vCenter yet.</div>`;

  $("#detailBody").innerHTML = `
    <div class="detail-head">
      <div>
        <p class="caps">vCenter</p>
        <h2 class="detail-title">${escapeHtml(vc.name)}</h2>
        <p class="detail-meta">${escapeHtml(vc.url)}</p>
        <p class="detail-meta">${escapeHtml(vc.datacenter)} / ${escapeHtml(vc.datastore)} · ${escapeHtml(vc.username)}${vc.insecure ? " · insecure" : ""}</p>
        <p class="detail-meta">ISO folder · ${escapeHtml(vc.isoFolder || "rf2vc/isos")}</p>
        ${vc.notes ? `<p class="detail-meta">${escapeHtml(vc.notes)}</p>` : ""}
      </div>
      <div class="detail-actions">
        <button type="button" class="pill ghost sm" data-edit="${vc.id}">Edit</button>
        <button type="button" class="pill danger sm" data-delete="${vc.id}">Delete</button>
      </div>
    </div>

    <div class="section-block">
      <div class="section-head">
        <h3>ISO cache</h3>
        <button type="button" class="pill ghost sm" data-refresh-iso="${vc.id}">Refresh</button>
      </div>
      <p class="detail-meta" id="isoSummary">Loading…</p>
      <div class="iso-list" id="isoList"></div>
      <p class="msg" id="isoMsg"></p>
    </div>

    <div class="section-block">
      <h3>UUID bindings (${rows.length})</h3>
      <div class="uuid-list">${uuidBlock}</div>
      <form class="bind-form" id="bindForm">
        <label>BIOS UUID <input name="uuid" required placeholder="4235a1b2-…" /></label>
        <label>Name <input name="name" placeholder="MO-OCLAB-CP01" /></label>
        <label>Notes <input name="notes" /></label>
        <button type="submit" class="pill primary">Bind UUID</button>
      </form>
      <p class="msg" id="bindMsg"></p>
    </div>
  `;

  loadISOStatus(vc.id);

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
    const body = {
      name: form.name.value.trim(),
      url: form.url.value.trim(),
      username: form.username.value.trim(),
      password: form.password.value,
      datacenter: form.datacenter.value.trim(),
      datastore: form.datastore.value.trim(),
      isoFolder: form.isoFolder.value.trim() || "rf2vc/isos",
      insecure: form.insecure.checked,
      notes: form.notes.value.trim(),
    };
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
    if (!form.id.value) {
      setMsg(msg, "Save the vCenter first, then Test.", false);
      return;
    }
    try {
      const res = await api(`/api/v1/vcenters/${form.id.value}/test`, {
        method: "POST",
        body: JSON.stringify({
          url: form.url.value.trim(),
          username: form.username.value.trim(),
          password: form.password.value,
          datacenter: form.datacenter.value.trim(),
          datastore: form.datastore.value.trim(),
          insecure: form.insecure.checked,
        }),
      });
      setMsg(msg, res.ok ? "Connection OK" : (res.error || "failed"), !!res.ok);
    } catch (err) {
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
  const edit = e.target.getAttribute("data-edit");
  const del = e.target.getAttribute("data-delete");
  const unmap = e.target.getAttribute("data-unmap");
  const refreshIso = e.target.getAttribute("data-refresh-iso");
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
  if (refreshIso) {
    await loadISOStatus(refreshIso);
  }
});

reload().catch(err => {
  $("#statusLine").textContent = "API error: " + err.message;
});
