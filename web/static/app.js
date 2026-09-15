const $ = (sel, el = document) => el.querySelector(sel);
const LANES = ["var(--lane-0)", "var(--lane-1)", "var(--lane-2)", "var(--lane-3)", "var(--lane-4)", "var(--lane-5)"];
const LANE_HEX = ["#c45c4a", "#d4894a", "#2f8f7d", "#976eb0", "#4a7fc5", "#8aa63a"];

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
  if (!el) return;
  el.textContent = text || "";
  el.className = "msg " + (ok === true ? "ok" : ok === false ? "err" : "");
}

function escapeHtml(s) {
  return String(s || "").replace(/[&<>"']/g, c => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
  }[c]));
}

let vcenters = [];
let mappings = [];
let selection = null; // { type: 'vc'|'uuid'|'form', id }

async function refreshStatus() {
  const st = await api("/api/v1/status");
  $("#statusLine").textContent =
    `${st.version} · ${st.vcenters} vCenter · ${st.mappings} UUID`;
}

function fillVCSelect() {
  const sel = $("#mapVCSelect");
  const cur = sel.value;
  sel.innerHTML = vcenters.map(v =>
    `<option value="${v.id}">${escapeHtml(v.name || v.url)}</option>`
  ).join("");
  if (cur) sel.value = cur;
}

function vcName(id) {
  return (vcenters.find(v => v.id === id) || {}).name || id;
}

function laneColor(i) {
  return LANE_HEX[i % LANE_HEX.length];
}

function renderMap() {
  const svg = $("#gatewayMap");
  const empty = $("#mapEmpty");
  if (!vcenters.length) {
    svg.innerHTML = "";
    empty.classList.remove("hidden");
    return;
  }
  empty.classList.add("hidden");

  const W = 640, H = 420;
  const cx = 200, cy = 210;
  const n = vcenters.length;
  const defs = `
    <defs>
      <radialGradient id="hubGrad" cx="35%" cy="30%" r="70%">
        <stop offset="0%" stop-color="#3f9f8e"/>
        <stop offset="100%" stop-color="#1f6f62"/>
      </radialGradient>
    </defs>`;

  let spokes = "";
  let nodes = "";
  vcenters.forEach((vc, i) => {
    const angle = -Math.PI / 2 + (i / Math.max(n, 1)) * Math.PI * 2 + (n === 1 ? Math.PI / 8 : 0);
    const r = n === 1 ? 150 : 155;
    const x = cx + Math.cos(angle) * r;
    const y = cy + Math.sin(angle) * r;
    const color = laneColor(i);
    const bound = mappings.filter(m => m.vcenterId === vc.id);
    spokes += `<path class="spoke" d="M${cx} ${cy} L${x} ${y}" stroke="${color}" />`;

    const label = (vc.name || "vCenter").slice(0, 16);
    const selected = selection?.type === "vc" && selection.id === vc.id;
    nodes += `
      <g class="lane-pill" data-vc="${vc.id}" transform="translate(${x}, ${y})">
        ${selected ? `<circle class="selected-ring" r="34" />` : ""}
        <rect x="-58" y="-18" width="116" height="36" rx="18" stroke="${color}" />
        <text y="5" fill="${color}">${escapeHtml(label)}</text>
      </g>`;

    bound.slice(0, 5).forEach((m, j) => {
      const a2 = angle + (j - (Math.min(bound.length, 5) - 1) / 2) * 0.22;
      const r2 = r + 78;
      const ux = cx + Math.cos(a2) * r2;
      const uy = cy + Math.sin(a2) * r2;
      const usel = selection?.type === "uuid" && selection.id === m.uuid;
      spokes += `<path class="spoke" d="M${x} ${y} L${ux} ${uy}" stroke="${color}" opacity="0.35" />`;
      nodes += `
        <g class="uuid-node" data-uuid="${escapeHtml(m.uuid)}" transform="translate(${ux}, ${uy})">
          <circle class="uuid-dot" r="${usel ? 8 : 6}" fill="${color}" />
          <text class="uuid-label" y="18">${escapeHtml((m.name || m.uuid).slice(0, 14))}</text>
        </g>`;
    });
  });

  svg.innerHTML = `
    ${defs}
    ${spokes}
    <g class="node-hub">
      <circle cx="${cx}" cy="${cy}" r="48" />
      <text x="${cx}" y="${cy + 5}">rf2vc</text>
    </g>
    ${nodes}
  `;
}

function showDetailPlaceholder() {
  $("#detailBody").innerHTML = `
    <div class="detail-placeholder">
      <div class="ph-icon" aria-hidden="true"></div>
      <p>Select a lane. vCenter forms, UUID binds, and deep-links appear here — map stays clean.</p>
    </div>`;
}

function showVCDetail(vc) {
  selection = { type: "vc", id: vc.id };
  const bound = mappings.filter(m => m.vcenterId === vc.id);
  $("#detailBody").innerHTML = `
    <h3 class="detail-title">${escapeHtml(vc.name)}</h3>
    <p class="detail-meta">${escapeHtml(vc.url)}</p>
    <p class="detail-meta">${escapeHtml(vc.datacenter)} / ${escapeHtml(vc.datastore)} · ${escapeHtml(vc.username)}${vc.insecure ? " · insecure" : ""}</p>
    <p class="detail-meta">${bound.length} UUID binding(s)</p>
    <div class="detail-actions">
      <button type="button" class="pill primary" data-edit-vc="${vc.id}">Edit</button>
      <button type="button" class="pill danger" data-del-vc="${vc.id}">Delete</button>
    </div>
    <div id="detailFormSlot"></div>
  `;
  renderMap();
}

function showUUIDDetail(m) {
  selection = { type: "uuid", id: m.uuid };
  $("#detailBody").innerHTML = `
    <h3 class="detail-title">${escapeHtml(m.name || "System")}</h3>
    <p class="detail-meta">${escapeHtml(m.uuid)}</p>
    <p class="detail-meta">→ ${escapeHtml(vcName(m.vcenterId))}</p>
    ${m.notes ? `<p class="detail-meta">${escapeHtml(m.notes)}</p>` : ""}
    <p class="detail-meta">Redfish: <code>/redfish/v1/Systems/${escapeHtml(m.uuid)}</code></p>
    <div class="detail-actions">
      <button type="button" class="pill danger" data-unmap="${escapeHtml(m.uuid)}">Remove binding</button>
    </div>
  `;
  renderMap();
}

function showVCForm(vc) {
  selection = { type: "form", id: vc?.id || "new" };
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

  $("#detailBody").innerHTML = `
    <h3 class="detail-title">${vc ? "Edit vCenter" : "Add vCenter"}</h3>
    <p class="detail-meta">GOVC-shaped fields. Passwords stay on the PVC map.</p>
    <div id="detailFormSlot"></div>
  `;
  $("#detailFormSlot").appendChild(form);
  setMsg($("#vcMsg"), vc ? "Leave password blank to keep." : "New endpoint", true);
  renderMap();
}

function renderMaps() {
  const q = ($("#mapFilter").value || "").toLowerCase();
  const rows = mappings.filter(m => {
    const hay = `${m.uuid} ${m.name || ""} ${m.notes || ""} ${vcName(m.vcenterId)}`.toLowerCase();
    return !q || hay.includes(q);
  });
  const box = $("#mapList");
  if (!rows.length) {
    box.innerHTML = `<div class="chip"><div class="meta">No UUID bindings${q ? " match" : " yet"}.</div></div>`;
    return;
  }
  box.innerHTML = rows.map(m => `
    <div class="chip" data-open-uuid="${escapeHtml(m.uuid)}">
      <div class="title">${escapeHtml(m.name || m.uuid)}</div>
      <div class="meta">${escapeHtml(m.uuid)}</div>
      <div class="meta">→ ${escapeHtml(vcName(m.vcenterId))}</div>
      <div class="row-actions">
        <button type="button" class="pill ghost" data-open-uuid="${escapeHtml(m.uuid)}">Open</button>
        <button type="button" class="pill danger" data-unmap="${escapeHtml(m.uuid)}">Remove</button>
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
  renderMaps();
  renderMap();
  await refreshStatus();
  if (selection?.type === "vc") {
    const vc = vcenters.find(v => v.id === selection.id);
    if (vc) showVCDetail(vc);
    else { selection = null; showDetailPlaceholder(); }
  } else if (selection?.type === "uuid") {
    const m = mappings.find(x => x.uuid === selection.id);
    if (m) showUUIDDetail(m);
    else { selection = null; showDetailPlaceholder(); }
  }
}

$("#btnNewVC").addEventListener("click", () => showVCForm(null));
$("#btnFocusMap").addEventListener("click", () => {
  $("#mapBlock").scrollIntoView({ behavior: "smooth", block: "start" });
});
$("#btnCancelVC").addEventListener("click", () => {
  const form = $("#vcForm");
  document.body.appendChild(form);
  form.classList.add("hidden");
  selection = null;
  showDetailPlaceholder();
  renderMap();
});

$("#gatewayMap").addEventListener("click", (e) => {
  const vcEl = e.target.closest("[data-vc]");
  const uuidEl = e.target.closest("[data-uuid]");
  if (vcEl) {
    const vc = vcenters.find(v => v.id === vcEl.getAttribute("data-vc"));
    if (vc) showVCDetail(vc);
  }
  if (uuidEl) {
    const m = mappings.find(x => x.uuid === uuidEl.getAttribute("data-uuid"));
    if (m) showUUIDDetail(m);
  }
});

$("#detailPane").addEventListener("click", async (e) => {
  const edit = e.target.getAttribute("data-edit-vc");
  const del = e.target.getAttribute("data-del-vc");
  const unmap = e.target.getAttribute("data-unmap");
  if (edit) {
    const vc = vcenters.find(v => v.id === edit);
    showVCForm(vc);
  }
  if (del) {
    if (!confirm("Delete this vCenter and its UUID mappings?")) return;
    await api(`/api/v1/vcenters/${del}`, { method: "DELETE" });
    selection = null;
    showDetailPlaceholder();
    await reload();
  }
  if (unmap) {
    await api(`/api/v1/mappings/${encodeURIComponent(unmap)}`, { method: "DELETE" });
    selection = null;
    showDetailPlaceholder();
    await reload();
  }
});

$("#mapList").addEventListener("click", async (e) => {
  const open = e.target.getAttribute("data-open-uuid") || e.target.closest("[data-open-uuid]")?.getAttribute("data-open-uuid");
  const unmap = e.target.getAttribute("data-unmap");
  if (unmap) {
    await api(`/api/v1/mappings/${encodeURIComponent(unmap)}`, { method: "DELETE" });
    await reload();
    return;
  }
  if (open) {
    const m = mappings.find(x => x.uuid === open);
    if (m) {
      showUUIDDetail(m);
      $("#mapBlock").scrollIntoView({ behavior: "smooth", block: "start" });
    }
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
    let out;
    if (f.id.value) {
      out = await api(`/api/v1/vcenters/${f.id.value}`, { method: "PUT", body: JSON.stringify(body) });
    } else {
      if (!body.password) throw new Error("password required for new vCenter");
      out = await api("/api/v1/vcenters", { method: "POST", body: JSON.stringify(body) });
    }
    document.body.appendChild(f);
    f.classList.add("hidden");
    await reload();
    const vc = vcenters.find(v => v.id === out.id) || out;
    showVCDetail(vc);
  } catch (err) {
    setMsg($("#vcMsg"), err.message, false);
  }
});

$("#btnTestVC").addEventListener("click", async () => {
  const f = $("#vcForm");
  if (!f.id.value) {
    setMsg($("#vcMsg"), "Save the vCenter first, then Test.", false);
    return;
  }
  const body = {
    url: f.url.value.trim(),
    username: f.username.value.trim(),
    password: f.password.value,
    datacenter: f.datacenter.value.trim(),
    datastore: f.datastore.value.trim(),
    insecure: f.insecure.checked,
  };
  try {
    const res = await api(`/api/v1/vcenters/${f.id.value}/test`, { method: "POST", body: JSON.stringify(body) });
    setMsg($("#vcMsg"), res.ok ? "Connection OK" : (res.error || "failed"), !!res.ok);
  } catch (err) {
    setMsg($("#vcMsg"), err.message, false);
  }
});

$("#mapForm").addEventListener("submit", async (e) => {
  e.preventDefault();
  const f = e.target;
  try {
    const out = await api("/api/v1/mappings", {
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
    showUUIDDetail(out);
  } catch (err) {
    setMsg($("#mapMsg"), err.message, false);
  }
});

$("#mapFilter").addEventListener("input", renderMaps);

reload().catch(err => {
  $("#statusLine").textContent = "API error: " + err.message;
});
