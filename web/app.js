// Leafbind interface. Talks to the local server over a token-guarded
// JSON API and receives job updates as server-sent events.
"use strict";

const $ = (sel, root = document) => root.querySelector(sel);

// The session token arrives in the URL once; keep it for reloads and take it
// out of the address bar.
const token = (() => {
  const t = new URLSearchParams(location.search).get("t");
  if (t) {
    try { sessionStorage.setItem("token", t); } catch {}
    history.replaceState(null, "", "/");
    return t;
  }
  try { return sessionStorage.getItem("token") || ""; } catch { return ""; }
})();

const store = {
  get(k, d) { try { const v = localStorage.getItem(k); return v === null ? d : JSON.parse(v); } catch { return d; } },
  set(k, v) { try { localStorage.setItem(k, JSON.stringify(v)); } catch {} },
};

async function api(path, opts = {}) {
  const res = await fetch(path, { ...opts, headers: { "X-Token": token, ...(opts.headers || {}) } });
  if (!res.ok) throw new Error((await res.text()).trim() || res.statusText);
  return res;
}
const withToken = (path) => path + (path.includes("?") ? "&" : "?") + "t=" + encodeURIComponent(token);

// ---------- state ----------

const state = { info: null, jobs: new Map(), order: [], seq: new Map() };
const RTL = new Set(["ar", "ur", "fa", "he", "ps", "sd", "ug", "yi", "dv", "ckb"]);

function fmtBytes(n) {
  if (n < 1024) return n + " B";
  const u = ["KB", "MB", "GB"]; let i = -1;
  do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1);
  return (n < 10 ? n.toFixed(1) : Math.round(n)) + " " + u[i];
}
const plural = (n, one, many = one + "s") => `${n} ${n === 1 ? one : many}`;
const icon = (id) => `<svg><use href="#i-${id}"/></svg>`;

// ---------- settings ----------

const els = {
  lang: $("#lang"), langOther: $("#lang-other"), langOtherRow: $("#lang-other-row"),
  flatten: $("#flatten"), orientation: $("#orientation"), dpi: $("#dpi"),
  quality: $("#quality"), qualityOut: $("#quality-out"), maxEdge: $("#maxEdge"),
  mixed: $("#mixed"), validate: $("#validate"), toc: $("#toc"),
};
let dirTouched = false;

function radios(name) { return [...document.querySelectorAll(`input[name="${name}"]`)]; }
function radioValue(name) { return (radios(name).find((r) => r.checked) || {}).value; }
function setRadio(name, value) { radios(name).forEach((r) => { r.checked = r.value === value; }); }

function readSettings() {
  const lang = els.lang.value === "other" ? els.langOther.value.trim() : els.lang.value;
  return {
    lang,
    direction: radioValue("direction"),
    grayscale: radioValue("colour") === "grey",
    flattenBG: els.flatten.checked,
    dpi: +els.dpi.value,
    quality: +els.quality.value,
    maxEdge: +els.maxEdge.value,
    orientation: els.orientation.value,
    mixed: els.mixed.checked,
    toc: els.toc.value,
    validate: els.validate.checked,
  };
}

function applySettings(s) {
  const known = [...els.lang.options].some((o) => o.value === s.lang);
  els.lang.value = known ? s.lang : "other";
  els.langOther.value = known ? "" : s.lang;
  els.langOtherRow.hidden = known;
  setRadio("direction", s.direction);
  setRadio("colour", s.grayscale ? "grey" : "colour");
  els.flatten.checked = s.flattenBG;
  els.orientation.value = s.orientation;
  els.dpi.value = [...els.dpi.options].some((o) => +o.value === s.dpi) ? String(s.dpi) : "0";
  els.quality.value = s.quality; els.qualityOut.value = s.quality;
  els.maxEdge.value = [...els.maxEdge.options].some((o) => +o.value === s.maxEdge) ? String(s.maxEdge) : "2560";
  els.mixed.checked = s.mixed;
  els.toc.value = s.toc === "pages" ? "pages" : "bookmarks";
  els.validate.checked = s.validate;
}

$("#settings").addEventListener("input", (e) => {
  if (e.target === els.lang) {
    els.langOtherRow.hidden = els.lang.value !== "other";
    if (els.lang.value === "other") els.langOther.focus();
    // Right-to-left scripts usually mean right-to-left page order, unless
    // the user has chosen the order themselves.
    const base = els.lang.value.split("-")[0];
    if (!dirTouched && els.lang.value !== "ur-Latn") setRadio("direction", RTL.has(base) ? "rtl" : "ltr");
  }
  if (e.target.name === "direction") dirTouched = true;
  if (e.target === els.quality) els.qualityOut.value = els.quality.value;
  store.set("settings", readSettings());
  store.set("dirTouched", dirTouched);
});
$("#settings").addEventListener("submit", (e) => e.preventDefault());
$("#reset").addEventListener("click", () => {
  if (!state.info) return;
  dirTouched = false;
  applySettings(state.info.defaults);
  store.set("settings", readSettings());
  store.set("dirTouched", false);
});

// ---------- theme ----------

const themes = ["auto", "light", "dark"];
function applyTheme(t) {
  if (t === "auto") document.documentElement.removeAttribute("data-theme");
  else document.documentElement.setAttribute("data-theme", t);
  const b = $("#theme");
  b.innerHTML = icon(t === "light" ? "sun" : t === "dark" ? "moon" : "auto");
  b.title = `Theme: ${t === "auto" ? "match system" : t}`;
}
let theme = store.get("theme", "auto");
applyTheme(theme);
$("#theme").addEventListener("click", () => {
  theme = themes[(themes.indexOf(theme) + 1) % themes.length];
  store.set("theme", theme);
  applyTheme(theme);
});

// ---------- toasts ----------

function toast(message, kind = "bad") {
  const t = document.createElement("div");
  t.className = "toast " + kind;
  t.innerHTML = icon(kind === "ok" ? "check" : "alert") + "<span></span>";
  t.querySelector("span").textContent = message;
  $("#toasts").append(t);
  setTimeout(() => t.remove(), 6000);
}

// ---------- rendering ----------

const stageText = {
  measuring: "Reading the PDF",
  rendering: "Rendering pages",
  packaging: "Packaging the EPUB",
  validating: "Validating",
  epubcheck: "Running epubcheck",
};
const badgeFor = {
  inspecting: ["Reading", ""],
  ready: ["Ready", ""],
  queued: ["Queued", ""],
  converting: ["Converting", "accent"],
  done: ["Done", "ok"],
  failed: ["Failed", "bad"],
};

const cards = new Map();

function card(job) {
  let li = cards.get(job.id);
  if (li) return li;
  li = $("#t-job").content.firstElementChild.cloneNode(true);
  li.dataset.id = job.id;
  const title = $(".title", li);
  title.addEventListener("keydown", (e) => {
    if (e.key === "Enter") title.blur();
    if (e.key === "Escape") { title.value = state.jobs.get(job.id).title; title.blur(); }
  });
  title.addEventListener("change", async () => {
    const v = title.value.trim();
    if (!v) { title.value = state.jobs.get(job.id).title; return; }
    try {
      await api(`/api/jobs/${job.id}`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ title: v }) });
    } catch (err) { toast(err.message); }
  });
  $(".remove", li).addEventListener("click", async () => {
    try { await api(`/api/jobs/${job.id}`, { method: "DELETE" }); } catch (err) { toast(err.message); }
  });
  $(".again", li).addEventListener("click", () => convert([job.id]));
  const pages = $(".pages", li);
  pages.addEventListener("keydown", (e) => { if (e.key === "Enter") pages.blur(); });
  pages.addEventListener("input", () => { pages.dataset.touched = "1"; });
  $(".unlock", li).addEventListener("submit", async (e) => {
    e.preventDefault();
    const pw = $(".password", li);
    if (!pw.value) { pw.focus(); return; }
    try {
      await api(`/api/jobs/${job.id}/unlock`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ password: pw.value }) });
      pw.value = "";
    } catch (err) { toast(err.message); }
  });
  $(".preview-btn", li).addEventListener("click", () => openPreview(job.id));
  $(".cover", li).addEventListener("click", () => { if (state.jobs.get(job.id).state === "done") openPreview(job.id); });
  cards.set(job.id, li);
  return li;
}

function renderJob(job) {
  const li = card(job);
  li.className = "job " + job.state;
  const [label, tone] = job.locked ? ["Locked", "warn"] : badgeFor[job.state] || [job.state, ""];
  const badge = $(".badge", li);
  badge.className = "badge " + tone;
  badge.innerHTML = (job.state === "done" ? icon("check") : job.locked ? icon("lock") : job.state === "failed" ? icon("alert") : "") + label;

  const title = $(".title", li);
  if (document.activeElement !== title) title.value = job.title;
  const busy = job.state === "queued" || job.state === "converting";
  title.disabled = busy;

  // A page range, once the page count is known; a password, for a locked PDF.
  const range = $(".range", li), pagesIn = $(".pages", li);
  range.hidden = !job.pages;
  pagesIn.disabled = busy;
  pagesIn.placeholder = job.pages ? `All ${job.pages}` : "All";
  if (document.activeElement !== pagesIn && !pagesIn.dataset.touched) pagesIn.value = job.pageRange || "";
  const unlock = $(".unlock", li);
  const wasHidden = unlock.hidden;
  unlock.hidden = !job.locked;
  if (job.locked && wasHidden) $(".password", li).focus();

  // Cover preview, shaped to the page's orientation.
  const cover = $(".cover", li), img = $("img", cover);
  cover.classList.toggle("landscape", (job.result ? job.result.orientation : job.orientation) === "landscape");
  if (job.hasCover && img.dataset.seq !== String(job.state === "done")) {
    img.src = withToken(`/api/jobs/${job.id}/cover`) + "&v=" + (job.state === "done" ? "final" : "preview");
    img.dataset.seq = String(job.state === "done");
    img.hidden = false;
    // SVG elements have no hidden property; set the attribute itself.
    $(".placeholder", cover).setAttribute("hidden", "");
  }

  const meta = [job.file];
  if (job.result && job.result.of && job.result.pages !== job.result.of) meta.push(`${job.result.pages} of ${plural(job.result.of, "page")}`);
  else if (job.pages) meta.push(plural(job.pages, "page"));
  meta.push(fmtBytes(job.bytes));
  const m = $(".meta", li);
  m.textContent = meta.join(" · ");
  if (job.scanPPI) {
    const s = document.createElement("span");
    s.className = "scan"; s.title = "Every page is a scanned image; automatic resolution keeps it native";
    s.innerHTML = icon("scan") + `Scan · ${job.scanPPI} ppi`;
    m.append(s);
  }

  // Progress.
  const bar = $(".progress", li), fill = $(".fill", bar), stage = $(".stage", li);
  bar.hidden = !busy;
  stage.hidden = !busy;
  if (busy) {
    // Reading the PDF is the first tenth of the bar, rendering the rest.
    const counted = (job.stage === "measuring" || job.stage === "rendering") && job.total > 0;
    const frac = !counted ? 0 : job.stage === "measuring" ? 0.1 * job.done / job.total : 0.1 + 0.9 * job.done / job.total;
    bar.classList.toggle("busy", !counted);
    fill.style.width = counted ? (100 * frac).toFixed(1) + "%" : "";
    stage.textContent = job.state === "queued" ? "Waiting for the book ahead"
      : counted ? `${stageText[job.stage]} · page ${job.done} of ${job.total}`
      : (stageText[job.stage] || "Starting");
  }

  // Result.
  const result = $(".result", li), r = job.result;
  result.hidden = !r;
  if (r) {
    const bits = [
      `<strong>${fmtBytes(r.bytes)}</strong>`,
      r.canvas.replace(" × ", " × "),
      r.orientation,
      `${r.dpi} DPI${r.scan ? " (native)" : ""}`,
      ...(r.toc ? [plural(r.toc, "contents entry", "contents entries")] : []),
      `${r.seconds < 10 ? r.seconds.toFixed(1) : Math.round(r.seconds)} s`,
    ];
    let v = "";
    if (r.validated) {
      v = r.passed
        ? ` · <span class="badge ok">${icon("check")}Valid${r.epubcheck === "passed" ? " · epubcheck" : ""}</span>`
        : ` · <span class="badge warn">${icon("alert")}Validation problems</span>`;
    }
    $(".stats", li).innerHTML = bits.join(" · ") + v;
    const rep = $(".report", li);
    rep.hidden = !(r.validated && !r.passed);
    if (!rep.hidden) {
      const total = (r.codes || []).reduce((a, c) => a + c.count, 0);
      $("summary", rep).textContent = `${plural(total, "problem")} — the book was kept so it can be inspected`;
      const ul = $("ul", rep);
      ul.replaceChildren(...(r.problems || []).map((p) => { const li = document.createElement("li"); li.textContent = p; return li; }));
    }
  }

  const err = $(".error", li);
  err.hidden = !job.error;
  err.classList.toggle("locked", !!job.locked);
  err.textContent = job.locked && job.error === "the PDF is password protected"
    ? "This PDF is password protected. Enter its password to open it." : job.error || "";

  li.classList.toggle("previewable", job.state === "done");
  $(".preview-btn", li).hidden = job.state !== "done";
  if (pv.id === job.id && (job.state !== "done" || (job.result && job.result.built !== pv.built))) closePreview();

  const dl = $(".download", li);
  dl.hidden = job.state !== "done";
  if (job.state === "done") dl.href = withToken(`/api/jobs/${job.id}/epub`);
  $(".again", li).hidden = !(job.state === "done" || (job.state === "failed" && job.pages));
}

function renderList() {
  const list = $("#list");
  const ids = state.order.filter((id) => state.jobs.has(id));
  for (const [id, li] of cards) if (!state.jobs.has(id)) { li.remove(); cards.delete(id); }
  ids.forEach((id, i) => {
    const li = card(state.jobs.get(id));
    if (list.children[i] !== li) list.insertBefore(li, list.children[i] || null);
  });

  const jobs = ids.map((id) => state.jobs.get(id));
  const count = (s) => jobs.filter((j) => j.state === s).length;
  const ready = count("ready"), done = count("done"), active = count("queued") + count("converting");
  const reading = count("inspecting");

  $(".files").classList.toggle("has-items", jobs.length > 0);
  $("#list-head").hidden = jobs.length === 0;
  $("#count").textContent = plural(jobs.length, "file");
  $("#download-all").hidden = done < 2;
  $("#download-all").href = withToken("/api/download-all");
  $("#clear-done").hidden = done === 0;

  const btn = $("#convert");
  btn.disabled = ready === 0 || !state.info || !state.info.engineReady;
  btn.textContent = ready ? `Convert ${plural(ready, "book")}` : "Convert";
  const parts = [];
  if (reading) parts.push(`reading ${reading}`);
  if (active) parts.push(`${active} converting`);
  if (done) parts.push(`${done} done`);
  if (ready) parts.push(`${ready} ready`);
  $("#summary").textContent = jobs.length === 0 ? "Add PDFs to begin." : parts.join(" · ") || "All done.";
}

function renderInfo() {
  const i = state.info;
  $("#engine").hidden = i.engineReady || !!i.engineError;
  if (i.engineError) toast("The PDF engine did not start: " + i.engineError);
  $("#about-version").textContent = "Version " + i.version;
  $("#licenses").href = withToken("/api/licenses");
  $("#epubcheck-note").textContent = i.epubcheck
    ? "Checks the finished EPUB, and runs epubcheck too."
    : "Checks the finished EPUB. Install epubcheck for a second opinion.";
}

// ---------- page preview ----------

// Kindle screens in pixels, held upright. A fixed-layout page is scaled to
// fit the screen, as the Kindle does, so the preview shows how much of the
// screen each page will fill.
const devices = [
  { id: "kindle", name: "Kindle (6″)", w: 1072, h: 1448 },
  { id: "paperwhite", name: "Kindle Paperwhite (7″)", w: 1264, h: 1680 },
  { id: "paperwhite-6.8", name: "Kindle Paperwhite (6.8″)", w: 1236, h: 1648 },
  { id: "colorsoft", name: "Kindle Colorsoft (7″)", w: 1264, h: 1680, colour: true },
  { id: "scribe", name: "Kindle Scribe (10.2″)", w: 1860, h: 2480 },
];
const pv = { id: null, built: 0, book: null, page: 0, lastPage: new Map() };
const pvEl = {
  dialog: $("#preview"), title: $("#pv-title"), sub: $("#pv-sub"), device: $("#pv-device"),
  rotate: $("#pv-rotate"), tocBtn: $("#pv-toc-btn"), toc: $("#pv-toc"), stage: $("#pv-stage"),
  frame: $("#pv-frame"), screen: $("#pv-screen"), img: $("#pv-img"), loading: $(".pv-loading"),
  left: $("#pv-left"), right: $("#pv-right"), slider: $("#pv-slider"), pos: $("#pv-pos"), fit: $("#pv-fit"),
};
pvEl.device.replaceChildren(...devices.map((d) => new Option(d.name, d.id)));
pvEl.device.value = devices.some((d) => d.id === store.get("pvDevice")) ? store.get("pvDevice") : "paperwhite";
let pvSideways = store.get("pvSideways", false);
let pvTocOpen = store.get("pvToc", true);

const pageURL = (i) => withToken(`/api/jobs/${pv.id}/pages/${i}`) + "&v=" + pv.built;

async function openPreview(id) {
  const job = state.jobs.get(id);
  if (!job || job.state !== "done") return;
  let book;
  try { book = await (await api(`/api/jobs/${id}/preview`)).json(); } catch (err) { toast(err.message); return; }
  Object.assign(pv, { id, built: job.result.built, book });
  pv.page = Math.min(pv.lastPage.get(id + ":" + pv.built) || 0, book.pages.length - 1);
  const rtl = book.direction === "rtl";

  pvEl.title.textContent = book.title || job.title;
  pvEl.sub.textContent = `${plural(book.pages.length, "page")} · ${rtl ? "right to left" : "left to right"}`;
  pvEl.slider.max = book.pages.length;
  pvEl.slider.dir = rtl ? "rtl" : "ltr";
  pvEl.left.setAttribute("aria-label", rtl ? "Next page" : "Previous page");
  pvEl.right.setAttribute("aria-label", rtl ? "Previous page" : "Next page");

  // The contents are worth a panel only when they came from bookmarks.
  const hasToc = !!(job.result.toc && book.toc && book.toc.length);
  pvEl.tocBtn.hidden = !hasToc;
  pvEl.toc.hidden = !(hasToc && pvTocOpen);
  pvEl.tocBtn.setAttribute("aria-pressed", String(hasToc && pvTocOpen));
  const list = (entries) => {
    const ol = document.createElement("ol");
    for (const e of entries) {
      const li = document.createElement("li");
      const b = document.createElement("button");
      b.type = "button"; b.dataset.page = e.page;
      b.innerHTML = "<span></span><small></small>";
      b.firstChild.textContent = e.title;
      b.lastChild.textContent = book.pages[e.page].label;
      b.addEventListener("click", () => showPage(e.page));
      li.append(b);
      if (e.children && e.children.length) li.append(list(e.children));
      ol.append(li);
    }
    return ol;
  };
  pvEl.toc.replaceChildren(hasToc ? list(book.toc) : document.createElement("ol"));

  if (!pvEl.dialog.open) pvEl.dialog.showModal();
  // Keys turn pages from the start, without a focus ring on the first button.
  pvEl.stage.focus();
  layoutPreview();
  showPage(pv.page);
}

function closePreview() {
  if (pvEl.dialog.open) pvEl.dialog.close();
}
pvEl.dialog.addEventListener("close", () => {
  if (pv.id) pv.lastPage.set(pv.id + ":" + pv.built, pv.page);
  pv.id = null; pv.book = null;
  pvEl.img.removeAttribute("src");
});

function device() {
  const d = devices.find((x) => x.id === pvEl.device.value) || devices[0];
  return pvSideways ? { ...d, w: d.h, h: d.w } : d;
}

// Size the Kindle to the space available, keeping its screen's proportions.
function layoutPreview() {
  if (!pv.book) return;
  const d = device();
  pvEl.rotate.setAttribute("aria-pressed", String(pvSideways));
  pvEl.screen.classList.toggle("colour", !!d.colour);
  // Leave room for the page-turn buttons, the gaps beside them and a margin.
  const beside = 2 * (pvEl.left.offsetWidth + 16) + 32;
  const availW = pvEl.stage.clientWidth - beside, availH = pvEl.stage.clientHeight - 32;
  const bezel = 0.055; // of the screen's shorter side, on every edge
  const short = Math.min(d.w, d.h);
  const scale = Math.max(0.05, Math.min(availW / (d.w + 2 * bezel * short), availH / (d.h + 2 * bezel * short)));
  pvEl.screen.style.width = Math.round(d.w * scale) + "px";
  pvEl.screen.style.height = Math.round(d.h * scale) + "px";
  pvEl.frame.style.padding = Math.round(bezel * short * scale) + "px";
  describeFit();
}

function describeFit() {
  const p = pv.book.pages[pv.page], d = device();
  if (!p.w || !p.h) { pvEl.fit.textContent = ""; return; }
  const k = Math.min(d.w / p.w, d.h / p.h);
  const fill = Math.round(100 * (p.w * k) * (p.h * k) / (d.w * d.h));
  let text = ` · fills ${fill}% of the screen`;
  const pageWide = p.w > p.h, screenWide = d.w > d.h;
  if (fill < 70 && pageWide !== screenWide) text += pageWide ? " — turn the Kindle sideways to read it larger" : " — hold the Kindle upright to read it larger";
  pvEl.fit.textContent = text;
}

function showPage(i) {
  const n = pv.book.pages.length;
  pv.page = Math.max(0, Math.min(n - 1, i));
  const p = pv.book.pages[pv.page];
  pvEl.loading.hidden = false;
  pvEl.img.onload = pvEl.img.onerror = () => { pvEl.loading.hidden = true; };
  pvEl.img.src = pageURL(pv.page);
  pvEl.img.alt = `Page ${p.label}`;
  pvEl.slider.value = pv.page + 1;
  const ordinal = `${pv.page + 1} of ${n}`;
  pvEl.pos.textContent = p.label === String(pv.page + 1) ? `Page ${ordinal}` : `Page ${p.label} · ${ordinal}`;
  describeFit();

  const rtl = pv.book.direction === "rtl";
  const atStart = pv.page === 0, atEnd = pv.page === n - 1;
  pvEl.left.disabled = rtl ? atEnd : atStart;
  pvEl.right.disabled = rtl ? atStart : atEnd;

  // Mark the section being read, and keep it in view.
  let current = null;
  for (const b of pvEl.toc.querySelectorAll("button")) {
    b.classList.remove("current");
    if (+b.dataset.page <= pv.page) current = b;
  }
  if (current) { current.classList.add("current"); current.scrollIntoView({ block: "nearest" }); }

  // Fetch the neighbours ahead of time, so turning a page is instant.
  for (const j of [pv.page + 1, pv.page - 1, pv.page + 2]) if (j >= 0 && j < n) new Image().src = pageURL(j);
}

// Turning towards the left goes back in a left-to-right book and forward in
// a right-to-left one, as on the Kindle.
function turn(side) {
  const forward = (side === "right") !== (pv.book.direction === "rtl");
  showPage(pv.page + (forward ? 1 : -1));
}
pvEl.left.addEventListener("click", () => turn("left"));
pvEl.right.addEventListener("click", () => turn("right"));
pvEl.screen.addEventListener("click", (e) => {
  const r = pvEl.screen.getBoundingClientRect();
  turn(e.clientX - r.left < r.width / 2 ? "left" : "right");
});
pvEl.slider.addEventListener("input", () => showPage(+pvEl.slider.value - 1));
pvEl.dialog.addEventListener("keydown", (e) => {
  if (!pv.book || e.target === pvEl.device || e.target === pvEl.slider) return;
  const keys = { ArrowLeft: () => turn("left"), ArrowRight: () => turn("right"), PageDown: () => showPage(pv.page + 1),
    PageUp: () => showPage(pv.page - 1), " ": () => showPage(pv.page + 1), Home: () => showPage(0), End: () => showPage(pv.book.pages.length - 1) };
  if (keys[e.key]) { e.preventDefault(); keys[e.key](); }
});
pvEl.device.addEventListener("change", () => { store.set("pvDevice", pvEl.device.value); layoutPreview(); });
pvEl.rotate.addEventListener("click", () => { pvSideways = !pvSideways; store.set("pvSideways", pvSideways); layoutPreview(); });
pvEl.tocBtn.addEventListener("click", () => {
  pvTocOpen = pvEl.toc.hidden;
  store.set("pvToc", pvTocOpen);
  pvEl.toc.hidden = !pvTocOpen;
  pvEl.tocBtn.setAttribute("aria-pressed", String(pvTocOpen));
  layoutPreview();
});
$("#pv-close").addEventListener("click", closePreview);
window.addEventListener("resize", () => { if (pvEl.dialog.open) layoutPreview(); });

// ---------- server events ----------

let stoppedTimer;
function connect() {
  const es = new EventSource(withToken("/api/events"));
  es.addEventListener("snapshot", (e) => {
    clearTimeout(stoppedTimer);
    const d = JSON.parse(e.data);
    const first = !state.info;
    state.info = d.info;
    state.jobs.clear(); state.order = [];
    for (const j of d.jobs) { state.jobs.set(j.id, j); state.order.push(j.id); }
    if (first) {
      applySettings({ ...d.info.defaults, ...store.get("settings", {}) });
      dirTouched = store.get("dirTouched", false);
    }
    renderInfo();
    state.jobs.forEach(renderJob);
    renderList();
  });
  es.addEventListener("info", (e) => { state.info = JSON.parse(e.data); renderInfo(); renderList(); });
  es.addEventListener("job", (e) => {
    const j = JSON.parse(e.data);
    if ((state.seq.get(j.id) || 0) > j.seq) return; // stale
    state.seq.set(j.id, j.seq);
    if (!state.jobs.has(j.id)) state.order.push(j.id);
    state.jobs.set(j.id, j);
    renderJob(j);
    renderList();
  });
  es.addEventListener("removed", (e) => {
    const { id } = JSON.parse(e.data);
    if (pv.id === id) closePreview();
    state.jobs.delete(id);
    state.order = state.order.filter((x) => x !== id);
    renderList();
  });
  es.addEventListener("quit", () => { es.close(); $("#stopped").hidden = false; });
  es.onerror = () => {
    // EventSource reconnects by itself; if the server is gone for good, say so.
    clearTimeout(stoppedTimer);
    stoppedTimer = setTimeout(() => { if (es.readyState !== EventSource.OPEN) { es.close(); $("#stopped").hidden = false; } }, 4000);
  };
}

// ---------- actions ----------

async function addFiles(fileList) {
  const files = [...fileList].filter((f) => /\.pdf$/i.test(f.name) || f.type === "application/pdf");
  const skipped = fileList.length - files.length;
  if (skipped) toast(`${plural(skipped, "file")} skipped: only PDFs can be converted.`);
  if (!files.length) return;
  const fd = new FormData();
  files.forEach((f) => fd.append("file", f, f.name));
  try { await api("/api/files", { method: "POST", body: fd }); } catch (err) { toast(err.message); }
}

async function convert(ids) {
  const settings = readSettings();
  if (!settings.lang) { toast("Enter a language tag, or choose a language."); els.langOther.focus(); return; }
  const titles = {}, pages = {};
  for (const id of ids) {
    const li = cards.get(id);
    if (!li) continue;
    titles[id] = $(".title", li).value.trim() || state.jobs.get(id).title;
    pages[id] = $(".pages", li).value.trim();
  }
  try {
    await api("/api/convert", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ids, settings, titles, pages }) });
  } catch (err) { toast(err.message); }
}

$("#convert").addEventListener("click", () => {
  convert(state.order.filter((id) => state.jobs.get(id).state === "ready"));
});
$("#picker").addEventListener("change", (e) => { addFiles(e.target.files); e.target.value = ""; });
$("#clear-done").addEventListener("click", async () => {
  for (const id of state.order.filter((id) => state.jobs.get(id).state === "done")) {
    try { await api(`/api/jobs/${id}`, { method: "DELETE" }); } catch {}
  }
});

// Dropping anywhere in the window adds files.
let dragDepth = 0;
const hasFiles = (e) => [...(e.dataTransfer?.types || [])].includes("Files");
window.addEventListener("dragenter", (e) => { if (!hasFiles(e)) return; e.preventDefault(); dragDepth++; $("#veil").hidden = false; });
window.addEventListener("dragover", (e) => { if (hasFiles(e)) e.preventDefault(); });
window.addEventListener("dragleave", (e) => { if (!hasFiles(e)) return; if (--dragDepth <= 0) { dragDepth = 0; $("#veil").hidden = true; } });
window.addEventListener("drop", (e) => {
  if (!hasFiles(e)) return;
  e.preventDefault(); dragDepth = 0; $("#veil").hidden = true;
  addFiles(e.dataTransfer.files);
});

window.addEventListener("keydown", (e) => {
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "o") { e.preventDefault(); $("#picker").click(); }
});

$("#about").addEventListener("click", () => $("#about-dialog").showModal());
$("#quit").addEventListener("click", async () => {
  const running = [...state.jobs.values()].some((j) => j.state === "queued" || j.state === "converting");
  if (running && !confirm("A book is still converting. Quit anyway?")) return;
  try { await api("/api/quit", { method: "POST" }); } catch {}
  $("#stopped").hidden = false;
});

if (!token) {
  $("#stopped").hidden = false;
  $("#stopped h2").textContent = "Open Leafbind from its link";
  $("#stopped p").textContent = "This page needs the address printed when the program starts.";
} else {
  $("#engine").hidden = false;
  connect();
}
