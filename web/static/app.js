(() => {
  "use strict";

  const LABELS = {
    pothole: "Pothole",
    road_crack: "Road crack",
    broken_surface: "Broken surface",
    waterlogging: "Waterlogging",
    debris: "Debris",
    road_edge_damage: "Edge damage",
    missing_lane_marking: "Missing marking",
    damaged_manhole: "Damaged manhole",
    speed_breaker: "Speed breaker",
  };
  const STATUS_LABELS = {
    open: "Open",
    acknowledged: "Acknowledged",
    in_progress: "In progress",
    resolved: "Resolved",
    rejected: "Rejected",
  };
  const SEV_COLORS = {
    critical: "#ff4d4f", high: "#ff8a3d", medium: "#f5b301", low: "#3fc1a5", unknown: "#7d8794",
  };
  const REFRESH_MS = 20000;

  const state = {
    status: "active",
    types: new Set(),
    defects: [],
    markers: new Map(),
    selected: null,
    fitted: false,
  };

  const $ = (id) => document.getElementById(id);
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const label = (t) => LABELS[t] || t;
  const fmtNum = (n) => (n == null ? "–" : Number(n).toLocaleString("en-IN"));

  function ago(iso) {
    const s = (Date.now() - new Date(iso).getTime()) / 1000;
    if (s < 60) return "just now";
    if (s < 3600) return Math.floor(s / 60) + "m ago";
    if (s < 86400) return Math.floor(s / 3600) + "h ago";
    return Math.floor(s / 86400) + "d ago";
  }
  function fmtDate(iso) {
    return new Date(iso).toLocaleString("en-IN", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });
  }
  function fmtHours(h) {
    if (h == null) return "–";
    if (h < 48) return h.toFixed(1) + " h";
    return (h / 24).toFixed(1) + " d";
  }

  async function api(path, opts = {}) {
    const res = await fetch(path, opts);
    const body = await res.json().catch(() => ({}));
    if (!res.ok) {
      const err = new Error(body.error || res.statusText);
      err.status = res.status;
      throw err;
    }
    return body;
  }

  // ---------- map ----------
  const map = L.map("map", { zoomControl: true, preferCanvas: true }).setView([22.6, 79.0], 5);
  L.tileLayer("https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 19,
    attribution: '&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors',
  }).addTo(map);

  function markerRadius(d) {
    return Math.min(16, 6 + Math.log2(1 + d.observation_count) * 2);
  }

  function renderMarkers() {
    const seen = new Set();
    for (const d of state.defects) {
      seen.add(d.defect_id);
      const color = SEV_COLORS[d.severity] || SEV_COLORS.unknown;
      const closed = d.status === "resolved" || d.status === "rejected";
      let m = state.markers.get(d.defect_id);
      const style = {
        radius: markerRadius(d), color: d.defect_id === state.selected ? "#ffffff" : "#121417",
        weight: d.defect_id === state.selected ? 3 : 1.5, fillColor: color, fillOpacity: closed ? 0.35 : 0.9,
      };
      if (!m) {
        m = L.circleMarker([d.latitude, d.longitude], style).addTo(map);
        m.on("click", () => select(d.defect_id));
        state.markers.set(d.defect_id, m);
      } else {
        m.setLatLng([d.latitude, d.longitude]).setStyle(style).setRadius(style.radius);
      }
      m.bindTooltip(`${esc(label(d.event_type))} · ${d.observation_count} report${d.observation_count === 1 ? "" : "s"}`);
    }
    for (const [id, m] of state.markers) {
      if (!seen.has(id)) {
        map.removeLayer(m);
        state.markers.delete(id);
      }
    }
  }

  // ---------- data ----------
  function query(withBBox) {
    const p = new URLSearchParams();
    if (state.status) p.set("status", state.status);
    if (state.types.size) p.set("type", [...state.types].join(","));
    if (withBBox) {
      const b = map.getBounds().pad(0.2);
      p.set("bbox", [b.getWest(), b.getSouth(), b.getEast(), b.getNorth()].map((n) => n.toFixed(5)).join(","));
    }
    p.set("limit", "2000");
    return p.toString();
  }

  let loading = false;
  async function loadDefects() {
    if (loading) return;
    loading = true;
    try {
      const firstFit = !state.fitted;
      const data = await api("/v1/defects?" + query(!firstFit));
      state.defects = data.defects;
      if (firstFit && state.defects.length) {
        const bounds = L.latLngBounds(state.defects.map((d) => [d.latitude, d.longitude]));
        map.fitBounds(bounds.pad(0.15), { maxZoom: 16 });
      }
      state.fitted = true;
      renderMarkers();
      renderList();
      setLive(true);
    } catch (e) {
      console.error(e);
      setLive(false);
    } finally {
      loading = false;
    }
  }

  async function loadStats() {
    try {
      const s = await api("/v1/stats");
      $("kpi-open").textContent = fmtNum(s.open_defects);
      $("kpi-resolved").textContent = fmtNum(s.resolved_defects);
      $("kpi-events").textContent = fmtNum(s.total_events);
      $("kpi-vehicles").textContent = fmtNum(s.vehicles);
      $("kpi-fix").textContent = fmtHours(s.avg_resolution_hours);
      renderLeaders(s);
    } catch (e) {
      console.error(e);
    }
  }

  function setLive(ok) {
    $("live").classList.toggle("stale", !ok);
    $("live-text").textContent = ok ? "Live" : "Offline";
  }

  // ---------- sidebar ----------
  function renderFilters() {
    const wrap = $("type-filter");
    wrap.innerHTML = "";
    for (const [t, name] of Object.entries(LABELS)) {
      const b = document.createElement("button");
      b.className = "chip" + (state.types.has(t) ? " on" : "");
      b.textContent = name;
      b.onclick = () => {
        state.types.has(t) ? state.types.delete(t) : state.types.add(t);
        renderFilters();
        loadDefects();
      };
      wrap.appendChild(b);
    }
  }
  $("status-filter").addEventListener("click", (e) => {
    const b = e.target.closest("button");
    if (!b) return;
    state.status = b.dataset.status;
    for (const x of $("status-filter").children) x.classList.toggle("on", x === b);
    loadDefects();
  });

  function renderList() {
    const list = $("defect-list");
    const items = [...state.defects].sort((a, b) =>
      (sevRank(b.severity) - sevRank(a.severity)) || (b.observation_count - a.observation_count));
    $("list-count").textContent = items.length;
    $("empty").hidden = items.length > 0;
    list.innerHTML = items.slice(0, 300).map((d) => {
      const who = d.responsibility.contractor_name || d.responsibility.authority_name || d.responsibility.road_name || "Responsibility not mapped";
      return `<li data-id="${esc(d.defect_id)}" class="${d.defect_id === state.selected ? "sel" : ""}">
        <span class="bar sev-${esc(d.severity)}"></span>
        <div><div class="t">${esc(label(d.event_type))}</div><div class="s">${esc(who)} · ${esc(ago(d.last_seen))}</div></div>
        <div class="n"><b>${d.observation_count}</b>${d.observation_count === 1 ? "report" : "reports"}</div>
      </li>`;
    }).join("");
  }
  $("defect-list").addEventListener("click", (e) => {
    const li = e.target.closest("li");
    if (li) select(li.dataset.id, true);
  });

  function sevRank(s) {
    return { critical: 4, high: 3, medium: 2, low: 1 }[s] || 0;
  }

  function renderLeaders(s) {
    const block = (title, rows) => rows && rows.length
      ? `<h3>${title}</h3>` + rows.slice(0, 5).map((r) => `<div class="row"><span>${esc(r.name)}</span><span>${r.open} open</span></div>`).join("")
      : "";
    $("leaders").innerHTML = block("Contractors with most open defects", s.top_contractors) +
      block("Authorities with most open defects", s.top_authorities);
  }

  // ---------- details ----------
  async function select(id, pan) {
    state.selected = id;
    renderMarkers();
    renderList();
    const drawer = $("drawer");
    drawer.classList.add("open");
    drawer.setAttribute("aria-hidden", "false");
    $("drawer-body").innerHTML = `<div class="d-body"><p class="empty">Loading…</p></div>`;
    try {
      const d = await api("/v1/defects/" + encodeURIComponent(id));
      if (pan) map.panTo([d.latitude, d.longitude]);
      renderDetail(d);
    } catch (e) {
      $("drawer-body").innerHTML = `<div class="d-body"><p class="empty">Could not load defect: ${esc(e.message)}</p></div>`;
    }
  }

  function closeDrawer() {
    state.selected = null;
    $("drawer").classList.remove("open");
    $("drawer").setAttribute("aria-hidden", "true");
    renderMarkers();
    renderList();
  }
  $("drawer-close").onclick = closeDrawer;
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") closeDrawer(); });

  function respRow(title, name, sub) {
    return `<div><dt>${title}</dt><dd>${name ? esc(name) : '<span class="none">Not mapped</span>'}${sub ? `<small>${esc(sub)}</small>` : ""}</dd></div>`;
  }

  function renderDetail(d) {
    const r = d.responsibility || {};
    const img = d.latest_image_uri;
    const images = (d.observations || []).filter((o) => o.image_uri).slice(0, 8);
    const nav = `https://www.google.com/maps/search/?api=1&query=${d.latitude},${d.longitude}`;
    const next = {
      open: ["acknowledged", "in_progress", "resolved", "rejected"],
      acknowledged: ["in_progress", "resolved", "rejected"],
      in_progress: ["resolved", "rejected"],
      resolved: ["open"],
      rejected: ["open"],
    }[d.status] || [];

    $("drawer-body").innerHTML = `
      <div class="d-hero" style="${img ? `background-image:url('${esc(img)}')` : ""}">
        ${img ? `<a href="${esc(img)}" target="_blank" rel="noopener" aria-label="Open image"></a>` : "No image captured"}
      </div>
      <div class="d-body">
        <div class="d-title">
          <h2>${esc(label(d.event_type))}</h2>
          <span class="pill sev-${esc(d.severity)}">${esc(d.severity)}</span>
          <span class="pill status">${esc(STATUS_LABELS[d.status] || d.status)}</span>
        </div>
        ${d.recurrence_of ? `<p class="empty" style="padding:0">Recurrence of a defect closed earlier at this spot.</p>` : ""}
        <dl class="facts">
          <div><dt>Reports</dt><dd>${d.observation_count}</dd></div>
          <div><dt>Vehicles</dt><dd>${d.vehicle_count}</dd></div>
          <div><dt>Confidence</dt><dd>${Math.round(d.max_confidence * 100)}%</dd></div>
          <div><dt>First seen</dt><dd>${esc(ago(d.first_seen))}</dd></div>
          <div><dt>Last seen</dt><dd>${esc(ago(d.last_seen))}</dd></div>
          <div><dt>Location</dt><dd><a href="${nav}" target="_blank" rel="noopener" style="color:var(--accent)">Map ↗</a></dd></div>
        </dl>

        <section class="card">
          <h3>Responsibility</h3>
          <dl class="resp">
            ${respRow("Road", r.road_name)}
            ${respRow("Authority", r.authority_name, r.division_name)}
            ${respRow("Contractor", r.contractor_name, r.contract_reference && "Contract " + r.contract_reference)}
            ${respRow("Officer", r.officer_name, r.officer_designation)}
            ${r.source_reference ? `<div><dt>Source</dt><dd><small>${esc(r.source_reference)}</small></dd></div>` : ""}
          </dl>
        </section>

        <section class="card">
          <h3>Remediation</h3>
          <div class="actions">
            <textarea id="note" placeholder="Note (work order, inspection remarks…)"></textarea>
            <div class="btns">${next.map((s, i) => `<button data-to="${s}" class="${i === 0 ? "primary" : ""}">${s === "open" ? "Reopen" : "Mark " + esc(STATUS_LABELS[s].toLowerCase())}</button>`).join("")}</div>
            <div class="msg" id="action-msg"></div>
          </div>
          ${d.history && d.history.length ? `<ol class="timeline">${d.history.map((h) => `
            <li><time>${esc(fmtDate(h.created_at))}</time>${esc(STATUS_LABELS[h.from_status] || h.from_status)} → <b>${esc(STATUS_LABELS[h.to_status] || h.to_status)}</b>${h.actor ? ` by ${esc(h.actor)}` : ""}${h.note ? `<br><span style="color:var(--muted)">${esc(h.note)}</span>` : ""}</li>`).join("")}</ol>` : ""}
        </section>

        ${images.length > 1 ? `<section class="card"><h3>Captures</h3><div class="thumbs">${images.map((o) => `<a href="${esc(o.image_uri)}" target="_blank" rel="noopener" style="background-image:url('${esc(o.image_uri)}')" title="${esc(fmtDate(o.detected_at))}"></a>`).join("")}</div></section>` : ""}

        <section class="card">
          <h3>Observations</h3>
          <ol class="timeline">${(d.observations || []).slice(0, 25).map((o) => `
            <li><time>${esc(fmtDate(o.detected_at))}</time>${esc(o.vehicle_id)} · ${esc(o.source)} · ${esc(o.severity)}${o.speed_kph ? ` · ${Math.round(o.speed_kph)} km/h` : ""}</li>`).join("")}</ol>
        </section>
      </div>`;

    for (const b of $("drawer-body").querySelectorAll("[data-to]")) {
      b.onclick = () => changeStatus(d.defect_id, b.dataset.to);
    }
  }

  async function changeStatus(id, to) {
    const msg = $("action-msg");
    const note = $("note").value.trim();
    const send = async () => {
      const headers = { "Content-Type": "application/json" };
      const key = localStorage.getItem("sadakwatch.adminKey");
      if (key) headers["X-API-Key"] = key;
      return api("/v1/defects/" + encodeURIComponent(id), {
        method: "PATCH", headers,
        body: JSON.stringify({ status: to, note, actor: localStorage.getItem("sadakwatch.actor") || "dashboard" }),
      });
    };
    msg.textContent = "Saving…";
    try {
      await send();
    } catch (e) {
      if (e.status !== 401) { msg.textContent = e.message; return; }
      const key = prompt("Admin API key required to change status:");
      if (!key) { msg.textContent = "Cancelled."; return; }
      localStorage.setItem("sadakwatch.adminKey", key);
      const actor = prompt("Your name (recorded in the history):", localStorage.getItem("sadakwatch.actor") || "");
      if (actor) localStorage.setItem("sadakwatch.actor", actor);
      try { await send(); } catch (e2) { msg.textContent = e2.message; return; }
    }
    await Promise.all([loadDefects(), loadStats()]);
    select(id);
  }

  // ---------- boot ----------
  renderFilters();
  let moveTimer;
  map.on("moveend", () => {
    if (!state.fitted) return;
    clearTimeout(moveTimer);
    moveTimer = setTimeout(loadDefects, 250);
  });
  loadDefects();
  loadStats();
  setInterval(() => { loadDefects(); loadStats(); }, REFRESH_MS);
})();
