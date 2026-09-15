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

function setMsg(el, text, ok) {
  el.textContent = text || "";
  el.className = "msg " + (ok === true ? "ok" : ok === false ? "err" : "");
}

let vcenters = [];
let mappings = [];

async function refreshStatus() {
  const st = await api("/api/v1/status");
  $("#statusLine").textContent =
    `${st.version} · ${st.vcenters} vCenter(s) · ${st.mappings} UUID mapping(s)`;
}

function fillVCSelect() {
  const sel = $("#mapVCSelect");
  const cur = sel.value;
  sel.innerHTML = vcenters.map(v =>
    `<option value="${v.id}">${escapeHtml(v.name || v.url)}</option>`
  ).join("");
  if (cur) sel.value = cur;
}

function escapeHtml(s) {
  return String(s || "").replace(/[&<>"']/g, c => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
  }[c]));
}

function renderVCs() {
  const box = $("#vcList");
  if (!vcenters.length) {
    box.innerHTML = `<div class="card"><div class="meta">No vCenters yet. Add one (GOVC-shaped fields).</div></div>`;
    return;
  }
  box.innerHTML = vcenters.map(v => `
    <div class="card" data-id="${v.id}">
      <div class="title">${escapeHtml(v.name)}</div>
      <div class="meta">${escapeHtml(v.url)} · ${escapeHtml(v.datacenter)} / ${escapeHtml(v.datastore)}</div>
      <div class="meta">${escapeHtml(v.username)}${v.insecure ? " · insecure" : ""}</div>
      <div class="row-actions">
        <button type="button" class="btn ghost" data-edit="${v.id}">Edit</button>
        <button type="button" class="btn danger" data-del="${v.id}">Delete</button>
      </div>
    </div>
  `).join("");
}

function renderMaps() {
  const q = ($("#mapFilter").value || "").toLowerCase();
  const vcName = id => (vcenters.find(v => v.id === id) || {}).name || id;
  const rows = mappings.filter(m => {
    const hay = `${m.uuid} ${m.name || ""} ${m.notes || ""} ${vcName(m.vcenterId)}`.toLowerCase();
    return !q || hay.includes(q);
  });
  const box = $("#mapList");
  if (!rows.length) {
    box.innerHTML = `<div class="card"><div class="meta">No UUID bindings${q ? " match" : " yet"}.</div></div>`;
    return;
  }
  box.innerHTML = rows.map(m => `
    <div class="card">
      <div class="title">${escapeHtml(m.name || m.uuid)}</div>
      <div class="meta">${escapeHtml(m.uuid)}</div>
      <div class="meta">→ ${escapeHtml(vcName(m.vcenterId))}</div>
      <div class="row-actions">
        <button type="button" class="btn danger" data-unmap="${escapeHtml(m.uuid)}">Remove</button>
      </div>
    </div>
  `).join("");
}

async function reload() {
  [vcenters, mappings] = await Promise.all([
    api("/api/v1/vcenters"),
    api("/api/v1/mappings"),
  ]);
  fillVCSelect();
  renderVCs();
  renderMaps();
  await refreshStatus();
}

function showVCForm(vc) {
  const form = $("#vcForm");
  form.classList.remove("hidden");
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
  setMsg($("#vcMsg"), vc ? "Editing — leave password blank to keep." : "New vCenter", true);
}

$("#btnNewVC").addEventListener("click", () => showVCForm(null));
$("#btnCancelVC").addEventListener("click", () => {
  $("#vcForm").classList.add("hidden");
  setMsg($("#vcMsg"), "");
});

$("#vcList").addEventListener("click", async (e) => {
  const edit = e.target.getAttribute("data-edit");
  const del = e.target.getAttribute("data-del");
  if (edit) {
    const vc = vcenters.find(v => v.id === edit);
    showVCForm(vc);
  }
  if (del) {
    if (!confirm("Delete this vCenter and its UUID mappings?")) return;
    await api(`/api/v1/vcenters/${del}`, { method: "DELETE" });
    await reload();
  }
});

$("#vcForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  const f = e.target;
  const body = {
    name: f.name.value.trim(),
    url: f.url.value.trim(),
    username: f.username.value.trim(),
    password: f.password.value,
    datacenter: f.datacenter.value.trim(),
    datastore: f.datastore.value.trim(),
    isoFolder: f.isoFolder.value.trim() || "rf2vc/isos",
    insecure: f.insecure.checked,
    notes: f.notes.value.trim(),
  };
  try {
    if (f.id.value) {
      await api(`/api/v1/vcenters/${f.id.value}`, { method: "PUT", body: JSON.stringify(body) });
    } else {
      if (!body.password) throw new Error("password required for new vCenter");
      await api("/api/v1/vcenters", { method: "POST", body: JSON.stringify(body) });
    }
    f.classList.add("hidden");
    setMsg($("#vcMsg"), "Saved", true);
    await reload();
  } catch (err) {
    setMsg($("#vcMsg"), err.message, false);
  }
});

$("#btnTestVC").addEventListener("click", async () => {
  const f = $("#vcForm");
  const body = {
    url: f.url.value.trim(),
    username: f.username.value.trim(),
    password: f.password.value,
    datacenter: f.datacenter.value.trim(),
    datastore: f.datastore.value.trim(),
    insecure: f.insecure.checked,
  };
  const id = f.id.value || "_";
  try {
    // For unsaved, POST to a temp path won't work — use existing id or create-less test via first saved
    if (!f.id.value) {
      setMsg($("#vcMsg"), "Save the vCenter first, then Test (or fill password and save).", false);
      return;
    }
    const res = await api(`/api/v1/vcenters/${id}/test`, { method: "POST", body: JSON.stringify(body) });
    setMsg($("#vcMsg"), res.ok ? "Connection OK" : (res.error || "failed"), !!res.ok);
  } catch (err) {
    setMsg($("#vcMsg"), err.message, false);
  }
});

$("#mapForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  const f = e.target;
  try {
    await api("/api/v1/mappings", {
      method: "POST",
      body: JSON.stringify({
        uuid: f.uuid.value.trim(),
        vcenterId: f.vcenterId.value,
        name: f.name.value.trim(),
        notes: f.notes.value.trim(),
      }),
    });
    f.reset();
    fillVCSelect();
    setMsg($("#mapMsg"), "Mapped", true);
    await reload();
  } catch (err) {
    setMsg($("#mapMsg"), err.message, false);
  }
});

$("#mapList").addEventListener("click", async (e) => {
  const u = e.target.getAttribute("data-unmap");
  if (!u) return;
  await api(`/api/v1/mappings/${encodeURIComponent(u)}`, { method: "DELETE" });
  await reload();
});

$("#mapFilter").addEventListener("input", renderMaps);

reload().catch(err => {
  $("#statusLine").textContent = "API error: " + err.message;
});
