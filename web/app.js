"use strict";

/* ---------- tiny DOM helpers (no innerHTML → no XSS from scraped data) ---------- */
const $ = (s, r = document) => r.querySelector(s);
const $$ = (s, r = document) => [...r.querySelectorAll(s)];

// el("div.cls#id", {attrs/props}, ...children). Children may be nodes or strings.
function el(spec, props, ...kids) {
  const m = spec.match(/^([a-z0-9]+)?([.#][^\s]*)?$/i) || [];
  const tag = m[1] || "div";
  const node = document.createElement(tag);
  if (m[2]) {
    for (const tok of m[2].match(/[.#][^.#]+/g) || []) {
      if (tok[0] === ".") node.classList.add(tok.slice(1));
      else node.id = tok.slice(1);
    }
  }
  if (props) {
    for (const [k, v] of Object.entries(props)) {
      if (v == null || v === false) continue;
      if (k === "class") node.className = v;
      else if (k === "text") node.textContent = v;
      else if (k === "html") {/* intentionally unsupported */}
      else if (k === "style" && typeof v === "object") Object.assign(node.style, v);
      else if (k === "dataset") Object.assign(node.dataset, v);
      else if (k.startsWith("on") && typeof v === "function") node.addEventListener(k.slice(2), v);
      else if (k === "checked" || k === "disabled" || k === "selected") node[k] = !!v;
      else node.setAttribute(k, v);
    }
  }
  for (const kid of kids.flat()) {
    if (kid == null || kid === false) continue;
    node.append(kid.nodeType ? kid : document.createTextNode(String(kid)));
  }
  return node;
}

const api = async (url, opts) => {
  const r = await fetch(url, opts);
  const data = r.headers.get("content-type")?.includes("json") ? await r.json() : null;
  if (!r.ok) throw new Error((data && data.error) || r.statusText);
  return data;
};
const brl = (n) => (n > 0 ? "R$ " + n.toLocaleString("pt-BR") : "Preço não informado");
// Human distance: metres under 1 km, else one-decimal km.
const fmtDist = (m) => (m >= 1000 ? (m / 1000).toFixed(1).replace(".", ",") + " km" : Math.round(m) + " m");
// Only allow http(s) URLs through; blocks javascript:/data: from scraped fields.
const safeURL = (u) => (/^https?:\/\//i.test(u || "") ? u : "");

let toastTimer;
function toast(msg, isErr) {
  const t = $("#toast");
  t.textContent = msg;
  t.className = "toast" + (isErr ? " err" : "");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.add("hidden"), 3200);
}

/* ---------- tabs ---------- */
$$(".tab").forEach((tab) =>
  tab.addEventListener("click", () => {
    $$(".tab").forEach((t) => t.classList.toggle("active", t === tab));
    const name = tab.dataset.tab;
    $$(".view").forEach((v) => v.classList.toggle("hidden", v.id !== "view-" + name));
    if (name === "sites") loadSites();
    if (name === "settings") loadSettings();
    if (name === "listings") { loadListings(); loadCities(); loadNeighborhoods(); }
  })
);

/* ---------- status bar ---------- */
async function refreshStatus() {
  try {
    const s = await api("/api/status");
    const st = s.stats;
    const bar = $("#stats");
    bar.replaceChildren(
      ...[["Total", st.total], ["New", st.new], ["Favorites", st.favorites], ["Active sites", st.sites]].map(([l, n]) =>
        el(".stat", null, el("span.n", { text: String(n) }), el("span.l", { text: l }))
      )
    );
    const sc = s.scheduler;
    $("#sched").textContent = sc.running ? "⏳ scraping…" : sc.message || "idle";
    $("#scrapeBtn").disabled = !!sc.running;
    return sc.running;
  } catch (e) { /* transient */ }
}

$("#scrapeBtn").addEventListener("click", async () => {
  $("#scrapeBtn").disabled = true;
  try {
    await api("/api/scrape", { method: "POST" });
    toast("Scrape started…");
    pollDuringScrape();
  } catch (e) { toast(e.message, true); $("#scrapeBtn").disabled = false; }
});

$("#reresolveMetroBtn").addEventListener("click", async () => {
  const btn = $("#reresolveMetroBtn");
  btn.disabled = true;
  try {
    const r = await api("/api/metro/reresolve", { method: "POST" });
    const parts = [];
    if (r.cleaned) parts.push(`cleaned ${r.cleaned} neighborhood(s)`);
    if (r.reset) parts.push(`re-resolving ${r.reset} unlocated listing(s) in the background`);
    toast(parts.length ? `${parts.join("; ")} — refresh in a bit.` : "Nothing to repair.");
    if (r.cleaned) loadNeighborhoods();
  } catch (e) { toast(e.message, true); }
  finally { btn.disabled = false; }
});

function pollDuringScrape() {
  let n = 0;
  const iv = setInterval(async () => {
    const running = await refreshStatus();
    if (!running || ++n > 200) { clearInterval(iv); loadListings(); loadCities(); loadNeighborhoods(); }
  }, 2000);
}

/* ---------- listings ---------- */
let listFilterTimer;
let favoritesOnly = false;
let mapView = false;        // listings shown as a map instead of the card grid
let listingsMap;            // current Leaflet map for the listings view, torn down on leave
let itemsById = new Map(); // id -> property, for the detail view
const DEFAULT_CITY = "São Paulo"; // pre-selected once the city is available
let cityDefaultApplied = false;
["#f-q", "#f-city", "#f-neigh", "#f-minprice", "#f-maxprice", "#f-minbeds", "#f-minarea", "#f-status", "#f-sort"].forEach((sel) =>
  $(sel).addEventListener("input", () => { clearTimeout(listFilterTimer); listFilterTimer = setTimeout(loadListings, 300); })
);
// A manual city choice (including "All cities") sticks — don't re-apply the
// São Paulo default over it on the next refresh.
$("#f-city").addEventListener("input", () => { cityDefaultApplied = true; });
$("#f-fav").addEventListener("click", () => {
  favoritesOnly = !favoritesOnly;
  $("#f-fav").textContent = (favoritesOnly ? "♥" : "♡") + " Favorites";
  $("#f-fav").classList.toggle("active-toggle", favoritesOnly);
  loadListings();
});
// Toggle between the card grid and a full map plotting every located listing in
// the current (filtered) result set. Reuses the same filters — flipping the view
// just re-renders the listings we already query.
$("#f-map").addEventListener("click", () => {
  mapView = !mapView;
  $("#f-map").textContent = (mapView ? "▤ Grid" : "🗺 Map");
  $("#f-map").setAttribute("aria-pressed", mapView ? "true" : "false");
  $("#f-map").classList.toggle("active-toggle", mapView);
  $("#grid").classList.toggle("hidden", mapView);
  $("#map").classList.toggle("hidden", !mapView);
  // Drop the Leaflet instance when leaving the map so we don't leak it.
  if (!mapView && listingsMap) { listingsMap.remove(); listingsMap = undefined; }
  loadListings();
});
$("#f-clear").addEventListener("click", () => {
  ["#f-q", "#f-neigh", "#f-minprice", "#f-maxprice", "#f-minbeds", "#f-minarea"].forEach((s) => ($(s).value = ""));
  $("#f-city").value = ""; $("#f-status").value = ""; $("#f-sort").value = "newest";
  cityDefaultApplied = true; // "Clear" means all cities — keep it that way
  if (favoritesOnly) { favoritesOnly = false; $("#f-fav").textContent = "♡ Favorites"; $("#f-fav").classList.remove("active-toggle"); }
  loadListings();
});

async function loadListings() {
  const p = new URLSearchParams({
    q: $("#f-q").value, city: $("#f-city").value, neighborhood: $("#f-neigh").value,
    min_price: $("#f-minprice").value, max_price: $("#f-maxprice").value,
    min_beds: $("#f-minbeds").value, min_area: $("#f-minarea").value,
    status: $("#f-status").value, sort: $("#f-sort").value,
    favorites: favoritesOnly ? "1" : "",
  });
  const grid = $("#grid");
  try {
    const items = await api("/api/properties?" + p);
    itemsById = new Map(items.map((it) => [String(it.id), it]));
    $("#count").textContent = items.length + " listing(s)";
    if (mapView) { renderListingsMap(items); return; }
    if (!items.length) {
      grid.replaceChildren(el(".empty", null, "No listings yet. Configure sites, set your filters, then hit ", el("b", { text: "Scrape now" }), "."));
      return;
    }
    grid.replaceChildren(...items.map(cardNode));
  } catch (e) {
    const target = mapView ? $("#map") : grid;
    target.replaceChildren(el(".empty", { text: "Error: " + e.message }));
  }
}

// renderListingsMap plots every located listing in the current result set on one
// OSM map, a pin per listing whose popup opens the full detail view. Listings not
// yet geocoded (latitude/longitude still 0) can't be placed, so we note how many
// of the total are actually on the map.
function renderListingsMap(items) {
  const mapEl = $("#map");
  if (listingsMap) { listingsMap.remove(); listingsMap = undefined; }
  mapEl.replaceChildren(); // clear any prior empty-state before Leaflet takes over

  if (typeof L === "undefined") {
    mapEl.replaceChildren(el(".empty", { text: "Map library failed to load." }));
    return;
  }
  const located = items.filter((p) => p.latitude && p.longitude);
  $("#count").textContent = `${items.length} listing(s) · ${located.length} on map`;
  if (!located.length) {
    mapEl.replaceChildren(el(".empty", null,
      "None of these listings are geocoded yet. Open a few to locate them, or run a ",
      el("b", { text: "Scrape now" }), " to resolve their locations."));
    return;
  }

  listingsMap = L.map(mapEl, { scrollWheelZoom: true, attributionControl: true });
  L.tileLayer("https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 19, attribution: "© OpenStreetMap",
  }).addTo(listingsMap);

  const pts = [];
  for (const p of located) {
    const pt = [p.latitude, p.longitude];
    pts.push(pt);
    const popup = el(".map-pop", null,
      el(".map-pop-price", { text: brl(p.price) }),
      el(".map-pop-title", { text: p.title || "(untitled listing)" }),
      el(".map-pop-addr", { text: p.neighborhood || p.address || "" }),
      el("button.btn.small", { text: "View details", onclick: () => { listingsMap.closePopup(); openDetail(p); } })
    );
    L.marker(pt).addTo(listingsMap).bindPopup(popup);
  }
  listingsMap.fitBounds(pts, { padding: [40, 40], maxZoom: 16 });
  // The container was display:none until this toggle; recompute size so tiles fill it.
  setTimeout(() => listingsMap && listingsMap.invalidateSize(), 60);
}

// loadCities (re)populates the city filter dropdown from the distinct
// municipalities the server has geocoded so far, preserving the current
// selection. Cities accrue as listings are geocoded, so this is refreshed on
// boot, when entering the Listings tab, and after a scrape.
async function loadCities() {
  try {
    const cities = await api("/api/cities");
    const sel = $("#f-city");
    const current = sel.value;
    sel.replaceChildren(
      el("option", { value: "", text: "All cities" }),
      ...cities.map((c) => el("option", { value: c, text: c, selected: c === current }))
    );
    sel.value = current; // keep selection even if it's no longer in the list
    // Default to São Paulo the first time it's available, unless the user has
    // already picked a city. Refilter so the grid matches the new selection.
    if (!cityDefaultApplied && current === "" && cities.includes(DEFAULT_CITY)) {
      cityDefaultApplied = true;
      sel.value = DEFAULT_CITY;
      loadListings();
    }
  } catch (e) { /* non-fatal */ }
}

// loadNeighborhoods (re)populates the neighborhood filter's datalist with the
// distinct neighborhoods the server has seen so far, giving the free-text input
// type-to-filter suggestions. Like cities, neighborhoods accrue as listings are
// scraped, so this is refreshed on boot, on entering Listings, and post-scrape.
async function loadNeighborhoods() {
  try {
    const neighborhoods = await api("/api/neighborhoods");
    $("#f-neigh-list").replaceChildren(
      ...neighborhoods.map((n) => el("option", { value: n }))
    );
  } catch (e) { /* non-fatal */ }
}

// metroChip renders the nearest-station badge (coloured by subway line) shown
// on cards and in the detail view. Returns null when no station is known yet.
function metroChip(p) {
  if (!p.metro_station) return null;
  return el(".metro", null,
    el("span.metro-dot", { style: { background: p.metro_color || "#888" } }),
    el("span.metro-name", { text: p.metro_station }),
    el("span.metro-dist", { text: fmtDist(p.metro_distance_m) })
  );
}

// Pick the most reliable thumbnail: a locally-downloaded photo (always loads,
// served from /photos) beats the scraped image_url, which is often missing
// (lazy-loaded list pages) or hotlink-protected.
function thumbURL(p) {
  if (p.thumb_path) return "/photos/" + p.thumb_path;
  return safeURL(p.image_url);
}

function cardNode(p) {
  const meta = [];
  if (p.bedrooms) meta.push("🛏 " + p.bedrooms);
  if (p.bathrooms) meta.push("🛁 " + p.bathrooms);
  if (p.parking_spots) meta.push("🚗 " + p.parking_spots);
  if (p.area_m2) meta.push("📐 " + p.area_m2 + " m²");
  const img = thumbURL(p);
  const thumb = img
    ? el(".thumb", { style: { backgroundImage: `url("${encodeURI(img)}")` } })
    : el(".thumb", { text: "🏠" });
  if (p.photo_count > 0) thumb.append(el(".photo-badge", { text: "📷 " + p.photo_count }));

  const favBtn = el("button.btn.small.fav", { dataset: { act: "favorite" }, text: p.favorite ? "♥ Liked" : "♡ Like" });
  if (p.favorite) favBtn.classList.add("active-toggle");

  const body = el(".body", null,
    p.status === "new" ? el("span.badge", { text: "NEW" }) : null,
    el(".price", { text: brl(p.price) }),
    el(".title", { text: p.title || "(untitled listing)" }),
    el(".addr", { text: p.neighborhood || p.address || "" }),
    metroChip(p),
    meta.length ? el(".meta", null, ...meta.map((m) => el("span", { text: m }))) : null,
    el(".src", { text: p.site_name }),
    el(".row", null,
      el("a.btn.small.open", { href: safeURL(p.url) || "#", target: "_blank", rel: "noopener", text: "Open ↗" }),
      favBtn,
      el("button.btn.small", { dataset: { act: "hidden" }, text: "Hide" })
    )
  );
  return el(".card.clickable", { dataset: { id: p.id, fav: p.favorite ? "1" : "0" } }, thumb, body);
}

$("#grid").addEventListener("click", async (e) => {
  const card = e.target.closest(".card");
  if (!card) return;
  const id = card.dataset.id;
  const btn = e.target.closest("button[data-act]");
  if (!btn) {
    // Click anywhere else on the card (but not the external "Open ↗" link)
    // opens the full local detail view.
    if (e.target.closest("a")) return;
    const p = itemsById.get(String(id));
    if (p) openDetail(p);
    return;
  }
  try {
    if (btn.dataset.act === "favorite") {
      await api(`/api/properties/${id}/favorite`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ favorite: card.dataset.fav !== "1" }),
      });
    } else {
      await api(`/api/properties/${id}/status`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ status: btn.dataset.act }),
      });
    }
    loadListings(); refreshStatus();
  } catch (err) { toast(err.message, true); }
});

/* ---------- detail view ---------- */
const fmtDate = (s) => { const d = new Date(s); return isNaN(+d) ? "—" : d.toLocaleString(); };

let detailMap; // current Leaflet map instance, torn down when the detail closes
let currentDetailId = null; // id of the listing currently shown in the detail view
let galleryImgs = [];       // the <img> nodes of the open listing, for j/k photo nav
let currentPhotoIndex = 0;  // which gallery photo is "current" (highlighted)

// metroShape normalises a property's metro fields into the same object the
// /metro endpoint returns, so the renderer takes one shape either way.
function metroShape(p) {
  return {
    checked: p.metro_checked, station: p.metro_station, line: p.metro_line,
    color: p.metro_color, distance_m: p.metro_distance_m,
    property: { lat: p.latitude, lon: p.longitude },
    metro: { lat: p.metro_lat, lon: p.metro_lon },
  };
}

// renderMetroMap draws the property and its nearest station on an OSM map with a
// line between them. Falls back to a text note when there's nothing to plot.
function renderMetroMap(mapEl, summaryEl, m) {
  const haveProp = m.property && m.property.lat && m.property.lon;
  const haveStation = m.station && m.metro && m.metro.lat && m.metro.lon;

  if (m.station) {
    summaryEl.replaceChildren(
      el("span.metro-dot", { style: { background: m.color || "#888" } }),
      el("b", { text: m.station }),
      el("span.muted", { text: " · " + (m.line || "") }),
      haveProp && haveStation ? el("span", { text: " · " + fmtDist(m.distance_m) + " away" }) : null
    );
  } else {
    summaryEl.replaceChildren(el("span.muted", {
      text: m.checked ? "Couldn’t locate this listing on the map." : "Locating nearest station…",
    }));
  }

  if (typeof L === "undefined" || !haveProp) { mapEl.style.display = "none"; return; }
  mapEl.style.display = "";

  const prop = [m.property.lat, m.property.lon];
  detailMap = L.map(mapEl, { scrollWheelZoom: false, attributionControl: true });
  L.tileLayer("https://{s}.tile.openstreetmap.org/{z}/{x}/{y}.png", {
    maxZoom: 19, attribution: "© OpenStreetMap",
  }).addTo(detailMap);

  L.marker(prop).addTo(detailMap).bindPopup("🏠 Property");

  if (haveStation) {
    const station = [m.metro.lat, m.metro.lon];
    const color = m.color || "#444";
    L.circleMarker(station, { radius: 8, color, fillColor: color, fillOpacity: 1, weight: 2 })
      .addTo(detailMap).bindPopup("🚇 " + m.station + " (" + (m.line || "") + ")");
    L.polyline([prop, station], { color, weight: 4, opacity: 0.7, dashArray: "6 6" }).addTo(detailMap);
    detailMap.fitBounds([prop, station], { padding: [40, 40] });
  } else {
    detailMap.setView(prop, 15);
  }
  // The map is created while the modal is animating in; recompute its size once
  // the container has settled so tiles fill it correctly.
  setTimeout(() => detailMap && detailMap.invalidateSize(), 60);
}

function factRow(label, value) {
  return el(".fact", null, el("span.fact-l", { text: label }), el("span.fact-v", { text: value || "—" }));
}

// openDetail renders everything we know about a listing locally — full photo
// gallery, all fields, and inline favorite/hide actions — without leaving the app.
async function openDetail(p) {
  // Tear down any prior map (e.g. when stepping through listings with j/k) so
  // we never leak a Leaflet instance bound to a now-detached container.
  if (detailMap) { detailMap.remove(); detailMap = undefined; }
  currentDetailId = String(p.id);
  galleryImgs = []; currentPhotoIndex = 0; // reset; repopulated once photos load

  const meta = [];
  if (p.bedrooms) meta.push("🛏 " + p.bedrooms + " bed");
  if (p.bathrooms) meta.push("🛁 " + p.bathrooms + " bath");
  if (p.parking_spots) meta.push("🚗 " + p.parking_spots + " parking");
  if (p.area_m2) meta.push("📐 " + p.area_m2 + " m²");

  const photoWrap = el(".detail-photos", null, el(".gallery-empty", { text: "Loading photos…" }));
  // On mobile photoWrap becomes a horizontal scroll-snap carousel; this counter
  // overlays it ("3 / 12") and tracks which photo is centred. Hidden on desktop
  // (CSS) and whenever there's 0–1 photo.
  const photoCounter = el(".carousel-counter.hidden");
  const gallery = el(".detail-gallery", null, photoWrap, photoCounter);
  const updateCounter = () => {
    if (galleryImgs.length > 1) {
      photoCounter.textContent = `${currentPhotoIndex + 1} / ${galleryImgs.length}`;
      photoCounter.classList.remove("hidden");
    } else {
      photoCounter.classList.add("hidden");
    }
  };
  // Keep the counter and highlight in sync as the carousel is swiped (mobile).
  // On desktop the photos stack vertically and photoWrap never scrolls, so this
  // listener stays dormant there.
  photoWrap.addEventListener("scroll", () => {
    if (!galleryImgs.length) return;
    const w = photoWrap.clientWidth || 1;
    const idx = Math.max(0, Math.min(galleryImgs.length - 1, Math.round(photoWrap.scrollLeft / w)));
    if (idx !== currentPhotoIndex) {
      currentPhotoIndex = idx;
      galleryImgs.forEach((im, i) => im.classList.toggle("active", i === idx));
      updateCounter();
    }
  }, { passive: true });

  const mapEl = el(".detail-map");
  const metroSummary = el(".metro-summary", null, el("span.muted", { text: "Locating nearest station…" }));
  const cityFact = factRow("City", p.city); // value updated once geocoding resolves

  const favBtn = el("button.btn.fav", { text: p.favorite ? "♥ Liked" : "♡ Like" });
  if (p.favorite) favBtn.classList.add("active-toggle");
  favBtn.addEventListener("click", async () => {
    try {
      await api(`/api/properties/${p.id}/favorite`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ favorite: !p.favorite }),
      });
      p.favorite = !p.favorite;
      favBtn.textContent = p.favorite ? "♥ Liked" : "♡ Like";
      favBtn.classList.toggle("active-toggle", p.favorite);
      loadListings(); refreshStatus();
    } catch (e) { toast(e.message, true); }
  });

  const hideBtn = el("button.btn", { text: p.status === "hidden" ? "Unhide" : "Hide" });
  hideBtn.addEventListener("click", async () => {
    try {
      await api(`/api/properties/${p.id}/status`, {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ status: p.status === "hidden" ? "seen" : "hidden" }),
      });
      closeDetail(); loadListings(); refreshStatus();
    } catch (e) { toast(e.message, true); }
  });

  $("#detailInner").replaceChildren(
    gallery,
    el(".detail-body", null,
      p.status === "new" ? el("span.badge", { text: "NEW" }) : null,
      el(".detail-price", { text: brl(p.price) }),
      el("h2.detail-title", { text: p.title || "(untitled listing)" }),
      el(".detail-addr", { text: [p.address, p.neighborhood].filter(Boolean).join(" · ") }),
      meta.length ? el(".detail-meta", null, ...meta.map((m) => el("span", { text: m }))) : null,
      el(".metro-block", null, el("h3.metro-h", { text: "🚇 Nearest subway station" }), metroSummary, mapEl),
      p.description ? el("p.detail-desc", { text: p.description }) : null,
      el(".detail-facts", null,
        factRow("Source", p.site_name),
        cityFact,
        factRow("Status", p.status),
        factRow("Favorite", p.favorite ? "Yes" : "No"),
        factRow("Photos", String(p.photo_count || 0)),
        factRow("First seen", fmtDate(p.first_seen)),
        factRow("Last seen", fmtDate(p.last_seen)),
      ),
      el(".detail-actions", null,
        safeURL(p.url) ? el("a.btn.primary", { href: safeURL(p.url), target: "_blank", rel: "noopener", text: "Open original ↗" }) : null,
        favBtn,
        hideBtn,
      ),
    )
  );
  $("#detail").classList.remove("hidden");
  $("#detail").scrollTop = 0; // start each listing at the top when navigating

  // Nearest station + map. Use what we already have; otherwise resolve on demand
  // (geocodes the address the first time, then it's cached on the server).
  (async () => {
    try {
      let m = metroShape(p);
      if (!p.metro_checked) {
        m = await api(`/api/properties/${p.id}/metro`);
        Object.assign(p, {
          metro_checked: m.checked, metro_station: m.station, metro_line: m.line,
          metro_color: m.color, metro_distance_m: m.distance_m, city: m.city,
          latitude: m.property.lat, longitude: m.property.lon,
          metro_lat: m.metro.lat, metro_lon: m.metro.lon,
        });
        const cityVal = cityFact.querySelector(".fact-v");
        if (cityVal) cityVal.textContent = p.city || "—";
      }
      renderMetroMap(mapEl, metroSummary, m);
    } catch (e) {
      metroSummary.replaceChildren(el("span.muted", { text: "Couldn’t load station info: " + e.message }));
      mapEl.style.display = "none";
    }
  })();

  try {
    const photos = await api(`/api/properties/${p.id}/photos`);
    if (photos.length) {
      const imgs = photos.map((ph) => el("img", { src: "/photos/" + ph.local_path, loading: "lazy", alt: "" }));
      photoWrap.replaceChildren(...imgs);
      galleryImgs = imgs;
    } else {
      const fallback = safeURL(p.image_url);
      const img = fallback ? el("img", { src: fallback, loading: "lazy", alt: "" }) : null;
      photoWrap.replaceChildren(img
        || el(".gallery-empty", { text: "No photos yet. Enable “Download listing photos” in Filters & Schedule, then run a scrape." }));
      galleryImgs = img ? [img] : [];
    }
  } catch (e) {
    photoWrap.replaceChildren(el(".gallery-empty", { text: "Error loading photos: " + e.message }));
    galleryImgs = [];
  }
  currentPhotoIndex = 0;
  if (galleryImgs[0]) galleryImgs[0].classList.add("active");
  updateCounter();
}
function closeDetail() {
  if (detailMap) { detailMap.remove(); detailMap = undefined; }
  currentDetailId = null;
  galleryImgs = []; currentPhotoIndex = 0;
  $("#detail").classList.add("hidden");
}

// navigateDetail steps to the previous/next listing in the current grid order
// while the detail view is open. delta is -1 (previous) or +1 (next). Clamps at
// the ends — itemsById is a Map, so its key order matches the rendered grid.
// Keyboard paging swaps instantly; the mobile touch handler drives the animated
// drag/slide directly (slideDetail / snapBackDetail below).
const detailMQ = window.matchMedia("(max-width: 860px)");
let detailSliding = false; // true while a commit/snap-back transition is in flight
function navigateDetail(delta) {
  const next = detailNeighbor(delta);
  if (next) openDetail(next);
}

// detailNeighbor returns the listing `delta` steps from the open one, or null at
// the ends / when the detail view is closed.
function detailNeighbor(delta) {
  if ($("#detail").classList.contains("hidden")) return null;
  const ids = [...itemsById.keys()];
  const i = ids.indexOf(String(currentDetailId));
  const j = i + delta;
  if (i === -1 || j < 0 || j >= ids.length) return null;
  return itemsById.get(ids[j]) || null;
}

// slideDetail finishes a swipe: it carries the current card the rest of the way
// off-screen in the swipe direction, swaps in the next listing, then slides it in
// from the opposite edge. Picks up from wherever the live drag left the card
// (clearing the drag's transition:none and locking that spot as the start frame),
// so the throw continues seamlessly from the finger. delta>0 (next) exits left.
function slideDetail(next, delta) {
  const inner = $("#detailInner");
  const outX = delta > 0 ? "-100%" : "100%"; // current card exits this way
  const inX = delta > 0 ? "100%" : "-100%";  // next card enters from here
  detailSliding = true;
  inner.classList.add("sliding");
  inner.style.transition = "";   // re-enable the .sliding transition (drag set it to none)
  void inner.offsetWidth;        // lock the current dragged position as the start frame
  inner.style.transform = `translateX(${outX})`;
  inner.style.opacity = "0";
  let swapped = false;
  const swap = () => {
    if (swapped) return;
    swapped = true;
    inner.removeEventListener("transitionend", swap);
    openDetail(next);                          // synchronous content swap
    inner.style.transition = "none";           // jump incoming to the opposite edge
    inner.style.transform = `translateX(${inX})`;
    inner.style.opacity = "0";
    void inner.offsetWidth;                     // force reflow so the slide-in animates
    inner.style.transition = "";                // hand back to .sliding's transition
    inner.style.transform = "translateX(0)";
    inner.style.opacity = "1";
    const cleanup = () => {
      inner.removeEventListener("transitionend", cleanup);
      inner.classList.remove("sliding");
      inner.style.transform = inner.style.opacity = inner.style.transition = "";
      detailSliding = false;
    };
    inner.addEventListener("transitionend", cleanup);
    setTimeout(cleanup, 320); // fallback if transitionend doesn't fire
  };
  inner.addEventListener("transitionend", swap);
  setTimeout(swap, 320); // fallback if the exit transition doesn't fire
}

// snapBackDetail eases the card back to rest when a drag is released without
// crossing the commit threshold (or there's no neighbour to page to).
function snapBackDetail() {
  const inner = $("#detailInner");
  detailSliding = true;
  inner.classList.add("sliding");
  inner.style.transition = "";
  void inner.offsetWidth; // lock the dragged position so the return animates
  inner.style.transform = "translateX(0)";
  inner.style.opacity = "1";
  const done = () => {
    inner.removeEventListener("transitionend", done);
    inner.classList.remove("sliding");
    inner.style.transform = inner.style.opacity = inner.style.transition = "";
    detailSliding = false;
  };
  inner.addEventListener("transitionend", done);
  setTimeout(done, 260); // fallback (also covers a near-zero drag that won't transition)
}

// navigatePhoto highlights and scrolls to the previous/next photo of the open
// listing. delta is -1 (previous) or +1 (next); clamps at the ends.
function navigatePhoto(delta) {
  if (!galleryImgs.length) return;
  const j = Math.min(galleryImgs.length - 1, Math.max(0, currentPhotoIndex + delta));
  // No-op only once the current photo is already highlighted at an end.
  if (j === currentPhotoIndex && galleryImgs[j].classList.contains("active")) return;
  currentPhotoIndex = j;
  galleryImgs.forEach((img, idx) => img.classList.toggle("active", idx === j));
  // block:center centres it in the vertical desktop stack; inline:center pages
  // the horizontal mobile carousel. Each axis is a no-op when there's nothing to
  // scroll, so one call serves both layouts.
  galleryImgs[j].scrollIntoView({ behavior: "auto", block: "center", inline: "center" });
}

$("#detailClose").addEventListener("click", closeDetail);
$("#detail").addEventListener("click", (e) => { if (e.target.id === "detail") closeDetail(); });
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") { closeDetail(); return; }
  if ($("#detail").classList.contains("hidden")) return;
  // Don't hijack typing (the detail view has no inputs today, but be safe).
  if (/^(INPUT|TEXTAREA|SELECT)$/.test(e.target.tagName)) return;
  switch (e.key) {
    case "j": case "ArrowDown": e.preventDefault(); navigatePhoto(1); break;   // next photo
    case "k": case "ArrowUp": e.preventDefault(); navigatePhoto(-1); break;    // previous photo
    case "l": case "ArrowRight": e.preventDefault(); navigateDetail(1); break;  // next property
    case "h": case "ArrowLeft": e.preventDefault(); navigateDetail(-1); break;  // previous property
  }
});

// Touch: on mobile the detail card is glued to the finger during a horizontal
// swipe (Tinder-style) — it drags live, then either completes the throw to the
// next/previous property or springs back. The photo carousel and the Leaflet map
// own their own horizontal gestures, so swipes that start on a *scrollable*
// carousel (photo paging) or on the map are left alone. On larger touch screens
// (no live drag) a horizontal flick still pages instantly.
let swipeX = 0, swipeY = 0, swiping = false, swipeAxis = null; // axis: "h" | "v" | null
let dragging = false; // true once a horizontal drag has the card glued to the finger
const detailEl = $("#detail");
const detailInner = () => $("#detailInner");

// While dragging, resist past the ends and fade slightly with distance so a throw
// telegraphs itself. dx is the raw finger delta; returns the pixels to translate.
function dragOffset(dx) {
  const atEnd = !detailNeighbor(dx < 0 ? 1 : -1);
  return atEnd ? dx * 0.3 : dx; // rubber-band when there's nowhere to page
}

detailEl.addEventListener("touchstart", (e) => {
  if (e.touches.length !== 1 || detailSliding) { swiping = false; return; }
  const car = e.target.closest(".detail-photos");
  if ((car && car.scrollWidth > car.clientWidth + 4) || e.target.closest(".detail-map")) {
    swiping = false; return;
  }
  swiping = true; swipeAxis = null; dragging = false;
  swipeX = e.touches[0].clientX; swipeY = e.touches[0].clientY;
}, { passive: true });
// Lock the gesture to an axis on first movement. Once it's horizontal, claim it
// with preventDefault so the browser doesn't steal it for its back/forward
// edge-swipe (which would navigate away from the page) — letting vertical pans
// scroll the overlay as normal. On mobile a horizontal lock starts a live drag.
detailEl.addEventListener("touchmove", (e) => {
  if (!swiping || e.touches.length !== 1) return;
  const dx = e.touches[0].clientX - swipeX, dy = e.touches[0].clientY - swipeY;
  if (!swipeAxis && (Math.abs(dx) > 10 || Math.abs(dy) > 10)) {
    swipeAxis = Math.abs(dx) > Math.abs(dy) ? "h" : "v";
    if (swipeAxis === "h" && detailMQ.matches) {
      dragging = true;
      detailInner().style.transition = "none"; // follow the finger with zero lag
    }
  }
  if (swipeAxis === "h") {
    e.preventDefault();
    if (dragging) {
      const off = dragOffset(dx);
      const inner = detailInner();
      inner.style.transform = `translateX(${off}px)`;
      inner.style.opacity = String(Math.max(0.5, 1 - Math.abs(off) / (window.innerWidth || 1)));
    }
  }
}, { passive: false });
detailEl.addEventListener("touchend", (e) => {
  if (!swiping) return;
  swiping = false;
  const t = e.changedTouches[0];
  const dx = t.clientX - swipeX, dy = t.clientY - swipeY;
  if (dragging) {
    dragging = false;
    e.preventDefault(); // suppress the click this gesture would otherwise synthesize
    const delta = dx < 0 ? 1 : -1; // swipe left → next, swipe right → previous
    const next = detailNeighbor(delta);
    const threshold = Math.min(110, (window.innerWidth || 320) * 0.28);
    if (next && Math.abs(dx) > threshold) slideDetail(next, delta); // commit the throw
    else snapBackDetail();                                          // didn't cross → spring back
    return;
  }
  // No live drag (larger touch screens): keep the instant horizontal-flick paging.
  if (Math.abs(dx) > 60 && Math.abs(dx) > Math.abs(dy) * 1.4) {
    e.preventDefault();
    navigateDetail(dx < 0 ? 1 : -1);
  }
}, { passive: false });
// A cancelled gesture (e.g. a system interruption) should not leave the card stuck
// off-centre — ease it back.
detailEl.addEventListener("touchcancel", () => {
  swiping = false;
  if (dragging) { dragging = false; snapBackDetail(); }
}, { passive: true });

/* ---------- sites ---------- */
let sites = [];
let selectedSiteId = null;

async function loadSites() {
  try { sites = await api("/api/sites"); renderSiteList(); }
  catch (e) { toast(e.message, true); }
}

function renderSiteList() {
  const list = $("#siteList");
  if (!sites.length) { list.replaceChildren(el("p.muted", { text: "No sites yet." })); return; }
  list.replaceChildren(...sites.map((s) => {
    const when = s.last_run && !String(s.last_run).startsWith("0001") ? new Date(s.last_run).toLocaleString() : "never run";
    const item = el(".site-item" + (s.id === selectedSiteId ? "" : ""), { dataset: { id: s.id } },
      el(".sname", null, el("span.dot." + (s.enabled ? "on" : "off")), s.name),
      el(".sstat.s-" + (s.last_status || "never"), { text: `${s.last_status} · ${s.last_found} found · ${when}` })
    );
    if (s.id === selectedSiteId) item.classList.add("active");
    item.addEventListener("click", () => editSite(sites.find((x) => x.id == s.id)));
    return item;
  }));
}

$("#newSiteBtn").addEventListener("click", () => editSite({
  id: 0, name: "", enabled: true, url_template: "", strategy: "css", max_pages: 1, notes: "",
  selectors: { item: "", title: "", url: "", attr_url: "href", image: "", attr_image: "src",
    price: "", address: "", neighborhood: "", bedrooms: "", bathrooms: "", parking_spots: "", area_m2: "", url_prefix: "" },
}));

const SEL_FIELDS = [
  ["item", "Item (list selector / array path) *"],
  ["title", "Title"], ["url", "URL"], ["url_prefix", "URL prefix (relative links)"],
  ["image", "Image"], ["price", "Price"], ["address", "Address"], ["neighborhood", "Neighborhood"],
  ["bedrooms", "Bedrooms"], ["bathrooms", "Bathrooms"], ["parking_spots", "Parking"], ["area_m2", "Area m²"],
  ["description", "Description"],
  ["latitude", "Latitude (optional)"], ["longitude", "Longitude (optional)"],
  ["detail_photos", "Detail-page gallery selector (photos)"],
  ["detail_photo_attr", "Photo attr (default src)"],
  ["photo_prefix", "Photo URL prefix"],
];

function labeledInput(labelText, attrs) {
  return el("label", { text: labelText }, el("input", attrs));
}

function editSite(s) {
  if (!s) return;
  selectedSiteId = s.id;
  renderSiteList();
  const sel = s.selectors || {};

  const strategySelect = el("select#e-strategy", null,
    el("option", { value: "css", selected: s.strategy === "css", text: "css — server-rendered HTML" }),
    el("option", { value: "nextdata", selected: s.strategy === "nextdata", text: "nextdata — embedded __NEXT_DATA__ JSON" })
  );
  const note = el("small#strategyNote", { style: { marginBottom: "10px" } });
  const setNote = () => {
    note.textContent = strategySelect.value === "nextdata"
      ? "NextData: 'item' is a dotted path to the listings array (e.g. props.pageProps.results.0.listings); field paths are relative to each item."
      : "CSS: 'item' is a card selector; field selectors are relative to it.";
  };
  strategySelect.addEventListener("change", setNote);

  const fieldGrid = el(".field-grid", null,
    ...SEL_FIELDS.map(([k, lbl]) => labeledInput(lbl, { "data-sel": k, value: sel[k] || "" })),
    labeledInput("Attr for URL", { "data-sel": "attr_url", value: sel.attr_url || "", placeholder: "href" }),
    labeledInput("Attr for Image", { "data-sel": "attr_image", value: sel.attr_image || "", placeholder: "src" })
  );

  const testOut = el("#testOut");
  const rowEnd = el(".row-end", null,
    s.id ? el("button.btn.danger", { text: "Delete", onclick: () => deleteSite(s.id) }) : null,
    el("button.btn", { text: "Test (live, first page)", onclick: () => testSite(testOut) }),
    el("button.btn.primary", { text: "Save", onclick: () => saveSite(s.id) })
  );

  $("#sitePane").replaceChildren(
    el("h3", { text: s.id ? "Edit site" : "New site" }),
    labeledInput("Name *", { id: "e-name", value: s.name }),
    el("label.inline", null, el("input#e-enabled", { type: "checkbox", checked: s.enabled }), " Enabled"),
    el("label.inline", null, el("input#e-jsrender", { type: "checkbox", checked: s.js_render }),
      " Render with headless browser (for JS / anti-bot sites — slower)"),
    el("label.inline", null, el("input#e-detailjs", { type: "checkbox", checked: s.detail_js_render }),
      " Render detail pages with browser too (for photo galleries)"),
    el("label", { text: "Strategy" }, strategySelect),
    labeledInput("URL template *", { id: "e-url", value: s.url_template, placeholder: "https://site/sp?max={maxPrice}&page={page}" }),
    el("small", { style: { margin: "-6px 0 10px", display: "block" }, text: "Placeholders: {query} {minPrice} {maxPrice} {minBeds} {minArea} {neighborhood} {page}" }),
    labeledInput("Max pages", { id: "e-pages", type: "number", min: "1", value: s.max_pages || 1, style: "width:90px" }),
    note,
    fieldGrid,
    el("label", { text: "Notes" }, el("textarea#e-notes", { text: s.notes })),
    rowEnd,
    testOut
  );
  setNote();
}

function collectSite(id) {
  const selectors = {};
  $$("#sitePane [data-sel]").forEach((elm) => (selectors[elm.dataset.sel] = elm.value.trim()));
  return {
    id,
    name: $("#e-name").value.trim(),
    enabled: $("#e-enabled").checked,
    js_render: $("#e-jsrender").checked,
    detail_js_render: $("#e-detailjs").checked,
    strategy: $("#e-strategy").value,
    url_template: $("#e-url").value.trim(),
    max_pages: parseInt($("#e-pages").value, 10) || 1,
    notes: $("#e-notes").value,
    selectors,
  };
}

async function saveSite(id) {
  try {
    await api("/api/sites", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(collectSite(id)),
    });
    toast("Site saved");
    const name = $("#e-name").value.trim();
    await loadSites();
    const fresh = sites.find((s) => s.name === name);
    if (fresh) editSite(fresh);
  } catch (e) { toast(e.message, true); }
}

async function deleteSite(id) {
  if (!confirm("Delete this site?")) return;
  try {
    await api(`/api/sites/${id}`, { method: "DELETE" });
    selectedSiteId = null;
    $("#sitePane").replaceChildren(el("p.muted.center", { text: "Site deleted." }));
    loadSites();
  } catch (e) { toast(e.message, true); }
}

async function testSite(out) {
  out.replaceChildren(el(".test-out", { text: "Testing…" }));
  try {
    const r = await api("/api/sites/test", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(collectSite(0)),
    });
    const box = el(".test-out", null, el("b", { text: `${r.count} item(s) parsed.` }));
    if (r.error) box.append(el(".s-error", { style: { margin: "6px 0" }, text: "⚠ " + r.error }));
    (r.sample || []).forEach((p) => {
      box.append(el(".test-row", null,
        `${brl(p.price)} — ${p.title || "(no title)"}`,
        el("br"),
        el("span.muted", { text: `${p.neighborhood || p.address} · 🛏${p.bedrooms} 📐${p.area_m2}m² · ${p.url}` })
      ));
    });
    out.replaceChildren(box);
  } catch (e) {
    out.replaceChildren(el(".test-out.s-error", { text: "Error: " + e.message }));
  }
}

/* ---------- settings ---------- */
async function loadSettings() {
  try {
    const s = await api("/api/settings");
    const f = s.filters || {};
    $("#s-query").value = f.query || "";
    $("#s-neigh").value = f.neighborhood || "";
    $("#s-minprice").value = f.min_price || "";
    $("#s-maxprice").value = f.max_price || "";
    $("#s-minbeds").value = f.min_bedrooms || "";
    $("#s-minarea").value = f.min_area_m2 || "";
    $("#s-interval").value = s.interval_minutes ?? 360;
    $("#s-delay").value = s.request_delay_seconds ?? 5;
    $("#s-dlphotos").checked = s.download_photos ?? true;
    $("#s-maxphotos").value = s.max_photos_per_listing ?? 0;
    $("#s-maxfetch").value = s.max_photo_fetches_per_run ?? 25;
    $("#s-maxmetro").value = s.max_metro_lookups_per_run ?? 50;
  } catch (e) { toast(e.message, true); }
}

$("#saveSettings").addEventListener("click", async () => {
  const body = {
    interval_minutes: parseInt($("#s-interval").value, 10) || 0,
    request_delay_seconds: parseInt($("#s-delay").value, 10) || 5,
    download_photos: $("#s-dlphotos").checked,
    max_photos_per_listing: parseInt($("#s-maxphotos").value, 10) || 0, // 0 = all
    max_photo_fetches_per_run: parseInt($("#s-maxfetch").value, 10) || 25,
    max_metro_lookups_per_run: parseInt($("#s-maxmetro").value, 10) || 50,
    filters: {
      query: $("#s-query").value.trim(),
      neighborhood: $("#s-neigh").value.trim(),
      min_price: parseInt($("#s-minprice").value, 10) || 0,
      max_price: parseInt($("#s-maxprice").value, 10) || 0,
      min_bedrooms: parseInt($("#s-minbeds").value, 10) || 0,
      min_area_m2: parseInt($("#s-minarea").value, 10) || 0,
    },
  };
  try {
    await api("/api/settings", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
    });
    toast("Settings saved");
  } catch (e) { toast(e.message, true); }
});

/* ---------- boot ---------- */
loadListings();
loadCities();
loadNeighborhoods();
refreshStatus();
setInterval(refreshStatus, 8000);
