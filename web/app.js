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
  mixed: $("#mixed"), validate: $("#validate"),
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
  cards.set(job.id, li);
  return li;
}

function renderJob(job) {
  const li = card(job);
  li.className = "job " + job.state;
  const [label, tone] = badgeFor[job.state] || [job.state, ""];
  const badge = $(".badge", li);
  badge.className = "badge " + tone;
  badge.innerHTML = (job.state === "done" ? icon("check") : job.state === "failed" ? icon("alert") : "") + label;

  const title = $(".title", li);
  if (document.activeElement !== title) title.value = job.title;
  const busy = job.state === "queued" || job.state === "converting";
  title.disabled = busy;

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
  if (job.pages) meta.push(plural(job.pages, "page"));
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
  err.textContent = job.error || "";

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
  const titles = {};
  for (const id of ids) {
    const li = cards.get(id);
    if (li) titles[id] = $(".title", li).value.trim() || state.jobs.get(id).title;
  }
  try {
    await api("/api/convert", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ids, settings, titles }) });
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
