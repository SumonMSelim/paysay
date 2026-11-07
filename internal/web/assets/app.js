// PaySay merchant workspace: navigation sidebar, invoice dashboard (AG Grid),
// and a docked voice assistant. All interface text comes from i18n.js so the
// whole panel follows the merchant's language.
"use strict";

const STRINGS = window.PAYSAY_STRINGS || {};
const VIEWS = ["overview", "invoices", "activity"];
const OPEN_STATUSES = new Set(["SENT", "UNPAID", "PARTIALLY_PAID"]);

const state = {
  locale: "en-US",
  locales: [],
  ttsEnabled: true,
  liveMode: false,
  history: [],
  recognizing: false,
  busy: false,
  view: "overview",
  invoices: [],
  mine: [],
  statusFilter: "",
  scopeMine: false,
  knownStatus: null, // Map of invoice id -> status from the previous load
};

const $ = (id) => document.getElementById(id);
const els = {
  brand: $("brand"),
  localeSelects: document.querySelectorAll(".locale-select"),
  modeBadge: $("mode-badge"),
  navCount: $("nav-invoice-count"),
  viewKicker: $("view-kicker"),
  viewTitle: $("view-title"),
  refresh: $("refresh"),
  chase: $("chase"),
  kpiOutstanding: $("kpi-outstanding"),
  kpiOutstandingHint: $("kpi-outstanding-hint"),
  kpiCollected: $("kpi-collected"),
  kpiCollectedHint: $("kpi-collected-hint"),
  kpiDrafts: $("kpi-drafts"),
  kpiRate: $("kpi-rate"),
  kpiRateBar: document.querySelector("#kpi-rate-bar i"),
  attention: $("attention"),
  recentGrid: $("recent-grid"),
  invoiceGrid: $("invoice-grid"),
  quickFilter: $("quick-filter"),
  statusFilter: $("status-filter"),
  scopeMine: $("scope-mine"),
  activity: $("activity"),
  activityEmpty: $("activity-empty"),
  assistant: $("assistant"),
  assistantSub: $("assistant-sub"),
  conversation: $("conversation"),
  welcome: $("welcome"),
  suggestions: $("suggestions"),
  messages: $("messages"),
  quickReplies: $("quick-replies"),
  newChat: $("new-chat"),
  loadError: $("load-error"),
  mic: $("mic"),
  micLabel: $("mic-label"),
  composer: $("composer"),
  message: $("message"),
  send: $("send"),
  status: $("status"),
  qrModal: $("qr-modal"),
  qrClose: $("qr-close"),
  qrImage: $("qr-image"),
  qrAmount: $("qr-amount"),
  qrId: $("qr-id"),
  qrLink: $("qr-link"),
};

let recentGrid = null;
let invoiceGrid = null;
let recognition = null;

// ---------- i18n ----------

function t(key, vars) {
  const table = STRINGS[state.locale] || {};
  let s = key in table ? table[key] : (STRINGS["en-US"] || {})[key];
  if (s === undefined) s = key;
  if (vars) for (const [k, v] of Object.entries(vars)) s = s.replace(`{${k}}`, v);
  return s;
}

function localeInfo() {
  return state.locales.find((l) => l.tag === state.locale) || {};
}

function applyStrings() {
  document.documentElement.lang = state.locale.split("-")[0];
  document.querySelectorAll("[data-i18n]").forEach((el) => (el.textContent = t(el.dataset.i18n)));
  document.querySelectorAll("[data-i18n-placeholder]").forEach((el) => (el.placeholder = t(el.dataset.i18nPlaceholder)));
  document.querySelectorAll("[data-i18n-aria]").forEach((el) => el.setAttribute("aria-label", t(el.dataset.i18nAria)));
  document.querySelectorAll("[data-i18n-title]").forEach((el) => (el.title = t(el.dataset.i18nTitle)));

  els.assistantSub.textContent = t("assistant.sub", { lang: localeInfo().native_name || "" });
  els.modeBadge.textContent = state.liveMode ? t("mode.live") : t("mode.demo");
  renderSuggestions();
  renderViewHeading();
  setMicState(state.recognizing);
  if (!recognition) els.micLabel.textContent = t("mic.unsupported");

  for (const api of [recentGrid, invoiceGrid]) {
    if (!api) continue;
    api.setGridOption("columnDefs", columnDefs(api === recentGrid));
    if (api === invoiceGrid) {
      restoreColumnState();
      updateTotalsRow(api);
    }
    api.setGridOption("overlayNoRowsTemplate", `<span class="grid-empty">${t("grid.empty")}</span>`);
  }
  renderDashboard();
}

// ---------- formatting ----------

function money(amount, currency) {
  const n = Number.parseFloat(amount);
  if (Number.isNaN(n)) return "";
  try {
    return new Intl.NumberFormat(state.locale, { style: "currency", currency: currency || "USD" }).format(n);
  } catch (_) {
    return `${n.toFixed(2)} ${currency || ""}`.trim();
  }
}

// shortDate omits the year for dates in the current year to keep columns narrow.
function shortDate(iso) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  const opts = { day: "numeric", month: "short" };
  if (d.getFullYear() !== new Date().getFullYear()) opts.year = "numeric";
  return new Intl.DateTimeFormat(state.locale, opts).format(d);
}

function timeOfDay(d) {
  return new Intl.DateTimeFormat(state.locale, { hour: "2-digit", minute: "2-digit" }).format(d);
}

// sumByCurrency renders per-currency totals, e.g. "€120.00 · ฿900.00".
function sumByCurrency(rows) {
  const totals = new Map();
  for (const r of rows) {
    const n = Number.parseFloat(r.amount);
    if (Number.isNaN(n)) continue;
    const cur = r.currency || "USD";
    totals.set(cur, (totals.get(cur) || 0) + n);
  }
  if (totals.size === 0) return money(0, localeInfo().currency || "USD");
  return [...totals].map(([cur, n]) => money(n, cur)).join(" · ");
}

function icon(name) {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "icon");
  const use = document.createElementNS("http://www.w3.org/2000/svg", "use");
  use.setAttribute("href", `#i-${name}`);
  svg.appendChild(use);
  return svg;
}

function el(tag, className, text) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
}

// ---------- initialization ----------

async function init() {
  await loadConfig();
  await loadLocales();
  setupSpeech();
  setupGrids();
  wireEvents();
  showView(VIEWS.includes(location.hash.slice(1)) ? location.hash.slice(1) : "overview");
  applyStrings();
  await refreshInvoices();
  restoreConversation();
}

async function loadConfig() {
  try {
    const cfg = await (await fetch("/api/config")).json();
    if (cfg.brand) {
      els.brand.textContent = cfg.brand;
      document.title = cfg.brand;
    }
    state.ttsEnabled = Boolean(cfg.tts_enabled);
    state.liveMode = Boolean(cfg.live_mode);
    state.locale = cfg.default_locale || state.locale;
    els.modeBadge.classList.add(state.liveMode ? "live" : "demo");
  } catch (_) {
    setStatus(t("error.generic"), true);
  }
}

async function loadLocales() {
  try {
    const data = await (await fetch("/api/locales")).json();
    state.locales = data.locales || [];
  } catch (_) {
    state.locales = [];
  }
  let saved = "";
  try {
    saved = localStorage.getItem("paysay.locale") || "";
  } catch (_) {}
  if (state.locales.some((l) => l.tag === saved)) state.locale = saved;

  for (const select of els.localeSelects) {
    select.innerHTML = "";
    for (const l of state.locales) {
      const opt = document.createElement("option");
      opt.value = l.tag;
      opt.textContent = l.native_name;
      select.appendChild(opt);
    }
    select.value = state.locale;
  }
}

function wireEvents() {
  els.localeSelects.forEach((select) => select.addEventListener("change", () => {
    state.locale = select.value;
    els.localeSelects.forEach((s) => (s.value = state.locale));
    try {
      localStorage.setItem("paysay.locale", state.locale);
    } catch (_) {}
    if (recognition) recognition.lang = localeInfo().speech_code || state.locale;
    applyStrings();
  }));

  document.querySelectorAll("[data-view]").forEach((b) =>
    b.addEventListener("click", () => showView(b.dataset.view)),
  );
  window.addEventListener("hashchange", () => {
    const v = location.hash.slice(1);
    if (VIEWS.includes(v)) showView(v);
  });

  document.querySelectorAll("[data-open-assistant]").forEach((b) => b.addEventListener("click", openAssistant));
  document.querySelectorAll("[data-close-assistant]").forEach((b) =>
    b.addEventListener("click", () => els.assistant.classList.remove("open")),
  );

  els.refresh.addEventListener("click", refreshInvoices);
  els.chase.addEventListener("click", () => ask(t("prompt.chase")));

  els.quickFilter.addEventListener("input", () =>
    invoiceGrid && invoiceGrid.setGridOption("quickFilterText", els.quickFilter.value),
  );
  els.statusFilter.querySelectorAll("button").forEach((b) =>
    b.addEventListener("click", () => setStatusFilter(b.dataset.status)),
  );
  els.scopeMine.addEventListener("change", () => {
    state.scopeMine = els.scopeMine.checked;
    refreshInvoices();
  });

  els.composer.addEventListener("submit", (e) => {
    e.preventDefault();
    const text = els.message.value.trim();
    if (text) sendMessage(text);
  });
  els.mic.addEventListener("click", toggleMic);
  els.newChat.addEventListener("click", newConversation);
  els.quickReplies.querySelectorAll("[data-reply]").forEach((b) =>
    b.addEventListener("click", () => sendMessage(t(b.dataset.reply === "yes" ? "prompt.yes" : "prompt.no"))),
  );

  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && els.assistant.classList.contains("open") && !els.qrModal.open) {
      els.assistant.classList.remove("open");
    }
  });
  els.qrClose.addEventListener("click", () => els.qrModal.close());
  els.qrModal.addEventListener("click", (e) => {
    if (e.target === els.qrModal) els.qrModal.close();
  });
}

// ---------- views ----------

function showView(view) {
  state.view = view;
  for (const v of VIEWS) $(`view-${v}`).hidden = v !== view;
  document.querySelectorAll(".nav-item[data-view]").forEach((b) => {
    if (b.dataset.view === view) b.setAttribute("aria-current", "page");
    else b.removeAttribute("aria-current");
  });
  if (location.hash.slice(1) !== view) history.replaceState(null, "", `#${view}`);
  renderViewHeading();
}

function renderViewHeading() {
  els.viewKicker.textContent = t(`view.${state.view}.kicker`);
  els.viewTitle.textContent = t(`view.${state.view}.title`);
}

function openAssistant() {
  els.assistant.classList.add("open");
  els.message.focus({ preventScroll: true });
}

// ---------- AG Grid ----------

function columnDefs(compact) {
  const cols = [
    {
      field: "id",
      headerName: t("col.invoice"),
      cellClass: "cell-id",
      minWidth: 120,
      flex: 0.9,
      valueFormatter: (p) => (p.node.rowPinned ? "" : p.value),
    },
    { field: "customer", headerName: t("col.customer"), minWidth: 140, flex: 1.4 },
    {
      field: "amount",
      headerName: t("col.amount"),
      type: "rightAligned",
      cellClass: (p) => (p.node.rowPinned ? "cell-amount cell-total" : "cell-amount"),
      // Mixed-currency totals need more room than one amount.
      colSpan: (p) => (p.node.rowPinned ? 3 : 1),
      minWidth: 112,
      flex: 0.9,
      comparator: (a, b) => Number.parseFloat(a || 0) - Number.parseFloat(b || 0),
      valueFormatter: (p) => {
        if (!p.data) return "";
        return p.node.rowPinned ? p.data.totalText : money(p.data.amount, p.data.currency);
      },
      getQuickFilterText: (p) => (p.data ? `${p.data.amount} ${money(p.data.amount, p.data.currency)}` : ""),
    },
    {
      field: "status",
      headerName: t("col.status"),
      minWidth: 112,
      flex: 0.8,
      cellRenderer: (p) => {
        const s = String(p.value || "").toUpperCase();
        return el("span", `pill pill-${s}`, t(`status.${s}`));
      },
      getQuickFilterText: (p) => t(`status.${String(p.value || "").toUpperCase()}`),
    },
    {
      field: "created",
      headerName: t("col.created"),
      minWidth: 116,
      flex: 0.8,
      sort: "desc",
      valueFormatter: (p) => (p.node.rowPinned ? "" : shortDate(p.value)),
    },
  ];
  if (compact) return cols.filter((c) => c.field !== "id");
  cols.push({
    headerName: t("col.actions"),
    colId: "actions",
    width: 96,
    cellClass: "cell-actions-host",
    sortable: false,
    filter: false,
    cellRenderer: (p) => (p.node.rowPinned ? "" : actionCell(p.data)),
  });
  return cols;
}

function actionCell(inv) {
  const wrap = el("div", "cell-actions");
  if (!inv || !inv.id) return wrap;
  const status = String(inv.status || "").toUpperCase();
  // Icon-only to fit the grid; the label is the tooltip and accessible name.
  const add = (label, iconName, onClick) => {
    const b = el("button", "icon-btn icon-btn-sm");
    b.type = "button";
    b.title = label;
    b.setAttribute("aria-label", `${label} ${inv.id}`);
    b.appendChild(icon(iconName));
    b.addEventListener("click", (e) => {
      e.stopPropagation();
      onClick();
    });
    wrap.appendChild(b);
  };
  if (status === "DRAFT") add(t("action.send"), "plane", () => ask(t("prompt.send", { id: inv.id })));
  if (OPEN_STATUSES.has(status)) {
    add(t("action.remind"), "bell", () => ask(t("prompt.remind", { id: inv.id })));
    add(t("action.qr"), "qr", () => showInvoiceQR(inv));
  }
  return wrap;
}

function gridTheme() {
  if (!agGrid.themeQuartz) return "legacy";
  return agGrid.themeQuartz.withParams({
    backgroundColor: "#ffffff",
    foregroundColor: "#1c1a16",
    headerBackgroundColor: "#fbfaf7",
    headerTextColor: "#57524a",
    headerFontWeight: 600,
    borderColor: "#e6e1d7",
    accentColor: "#1f3fd1",
    rowHoverColor: "#f6f4ef",
    oddRowBackgroundColor: "#ffffff",
    fontFamily: getComputedStyle(document.body).fontFamily,
    fontSize: 14,
    headerFontSize: 13,
    rowHeight: 48,
    headerHeight: 42,
    wrapperBorderRadius: 10,
    columnBorder: false,
    valueChangeValueHighlightBackgroundColor: "#e9edfc",
  });
}

function setupGrids() {
  if (!window.agGrid) return;
  if (agGrid.ModuleRegistry && agGrid.AllCommunityModule) {
    agGrid.ModuleRegistry.registerModules([agGrid.AllCommunityModule]);
  }
  const base = {
    theme: gridTheme(),
    rowData: [],
    getRowId: (p) => p.data.id,
    // Filtering is done with the search box and status buttons, which are
    // easier to use than per-column filter menus.
    defaultColDef: { sortable: true, resizable: true, suppressHeaderMenuButton: true, suppressHeaderFilterButton: true },
    enableCellTextSelection: true,
    overlayNoRowsTemplate: `<span class="grid-empty">${t("grid.empty")}</span>`,
  };
  recentGrid = agGrid.createGrid(els.recentGrid, {
    ...base,
    domLayout: "autoHeight",
    columnDefs: columnDefs(true),
  });
  invoiceGrid = agGrid.createGrid(els.invoiceGrid, {
    ...base,
    columnDefs: columnDefs(false),
    onGridReady: () => setTimeout(restoreColumnState),
    isExternalFilterPresent: () => state.statusFilter !== "",
    doesExternalFilterPass: (node) => statusMatches(node.data.status, state.statusFilter),
    onModelUpdated: (e) => updateTotalsRow(e.api),
    onSortChanged: saveColumnState,
    onColumnMoved: saveColumnState,
    onColumnResized: (e) => e.finished && saveColumnState(),
  });
}

// statusMatches applies the status filter. OPEN groups every status that is
// still waiting for money.
function statusMatches(status, filter) {
  const s = String(status || "").toUpperCase();
  return filter === "OPEN" ? OPEN_STATUSES.has(s) : s === filter;
}

function setStatusFilter(value) {
  state.statusFilter = value;
  els.statusFilter
    .querySelectorAll("button")
    .forEach((x) => x.setAttribute("aria-pressed", String(x.dataset.status === value)));
  if (invoiceGrid) invoiceGrid.onFilterChanged();
}

// updateTotalsRow pins a totals row under the invoice table that always
// reflects the rows currently shown (after search and filters).
function updateTotalsRow(api) {
  const rows = [];
  api.forEachNodeAfterFilter((node) => node.data && rows.push(node.data));
  api.setGridOption("pinnedBottomRowData", rows.length
    ? [{
      id: "__total",
      customer: t(rows.length === 1 ? "grid.totalOne" : "grid.total", { n: rows.length }),
      totalText: sumByCurrency(rows),
    }]
    : []);
}

// The invoice table remembers column order, widths and sorting per browser.
const GRID_STATE_KEY = "paysay.grid";

function saveColumnState() {
  if (!invoiceGrid) return;
  try {
    localStorage.setItem(GRID_STATE_KEY, JSON.stringify(invoiceGrid.getColumnState()));
  } catch (_) {}
}

function restoreColumnState() {
  if (!invoiceGrid) return;
  try {
    const saved = JSON.parse(localStorage.getItem(GRID_STATE_KEY) || "null");
    if (Array.isArray(saved)) invoiceGrid.applyColumnState({ state: saved, applyOrder: true });
  } catch (_) {}
}

// applyShowInvoices carries out the assistant's show_invoices call: it opens
// the invoice table with the requested status and customer filter.
function applyShowInvoices(call) {
  const input = parseJSON(call.input) || {};
  const status = String(input.status || "ALL").toUpperCase();
  setStatusFilter(["DRAFT", "OPEN", "PAID"].includes(status) ? status : "");
  els.quickFilter.value = input.customer || "";
  if (invoiceGrid) invoiceGrid.setGridOption("quickFilterText", els.quickFilter.value);
  els.assistant.classList.remove("open");
  showView("invoices");
}

async function fetchInvoices(scope) {
  const res = await fetch(scope ? `/api/invoices?scope=${scope}` : "/api/invoices");
  if (!res.ok) throw new Error(`invoices ${res.status}`);
  return (await res.json()).invoices || [];
}

async function refreshInvoices() {
  try {
    const [all, mine] = await Promise.all([
      fetchInvoices(""),
      state.scopeMine ? fetchInvoices("mine") : Promise.resolve(null),
    ]);
    state.invoices = all;
    state.mine = mine;
    els.loadError.hidden = true;
  } catch (_) {
    els.loadError.hidden = false;
    return;
  }

  const changed = changedInvoiceIds(state.invoices);
  if (recentGrid) recentGrid.setGridOption("rowData", state.invoices.slice(0, 6));
  if (invoiceGrid) invoiceGrid.setGridOption("rowData", state.scopeMine ? state.mine : state.invoices);
  flashRows(changed);
  renderDashboard();
}

// changedInvoiceIds returns invoices that are new or changed status since the
// last load, so the grid can highlight what the assistant just did.
function changedInvoiceIds(rows) {
  const next = new Map(rows.map((r) => [r.id, r.status]));
  const prev = state.knownStatus;
  state.knownStatus = next;
  if (!prev) return [];
  return rows.filter((r) => prev.get(r.id) !== r.status).map((r) => r.id);
}

function flashRows(ids) {
  if (ids.length === 0) return;
  for (const api of [recentGrid, invoiceGrid]) {
    if (!api) continue;
    const nodes = ids.map((id) => api.getRowNode(id)).filter(Boolean);
    if (nodes.length) api.flashCells({ rowNodes: nodes, flashDuration: 1600, fadeDuration: 900 });
  }
}

// ---------- overview ----------

function renderDashboard() {
  const rows = state.invoices;
  const open = rows.filter((r) => OPEN_STATUSES.has(String(r.status).toUpperCase()));
  const paid = rows.filter((r) => String(r.status).toUpperCase() === "PAID");
  const drafts = rows.filter((r) => String(r.status).toUpperCase() === "DRAFT");

  els.navCount.textContent = String(rows.length);
  els.kpiOutstanding.textContent = sumByCurrency(open);
  els.kpiOutstandingHint.textContent = t("kpi.outstandingHint", { n: open.length });
  els.kpiCollected.textContent = sumByCurrency(paid);
  els.kpiCollectedHint.textContent = t("kpi.collectedHint", { n: paid.length });
  els.kpiDrafts.textContent = new Intl.NumberFormat(state.locale).format(drafts.length);

  const billed = open.length + paid.length;
  const rate = billed ? paid.length / billed : 0;
  els.kpiRate.textContent = billed ? new Intl.NumberFormat(state.locale, { style: "percent" }).format(rate) : "–";
  els.kpiRateBar.style.width = `${Math.round(rate * 100)}%`;

  renderAttention(open, drafts);
}

function renderAttention(open, drafts) {
  els.attention.innerHTML = "";
  const byAge = (a, b) => String(a.created).localeCompare(String(b.created));
  const items = [...open.sort(byAge), ...drafts.sort(byAge)].slice(0, 5);
  if (items.length === 0) {
    els.attention.appendChild(el("li", "attention-empty", t("attention.empty")));
    return;
  }
  for (const inv of items) {
    const isDraft = String(inv.status).toUpperCase() === "DRAFT";
    const li = el("li");
    const main = el("div", "attention-main");
    main.append(
      el("div", "attention-who", inv.customer || inv.id),
      el("div", "attention-what", t(isDraft ? "attention.draft" : "attention.sent")),
    );
    const btn = el("button", "btn-sm");
    btn.type = "button";
    btn.append(icon(isDraft ? "plane" : "bell"), document.createTextNode(t(isDraft ? "action.send" : "action.remind")));
    btn.addEventListener("click", () => ask(t(isDraft ? "prompt.send" : "prompt.remind", { id: inv.id })));
    li.append(main, el("span", "attention-amount", money(inv.amount, inv.currency)), btn);
    els.attention.appendChild(li);
  }
}

// ---------- QR ----------

async function showInvoiceQR(inv) {
  els.qrId.textContent = inv.id;
  els.qrAmount.textContent = money(inv.amount, inv.currency) || "–";
  els.qrLink.href = `https://www.sandbox.paypal.com/invoice/p/#${encodeURIComponent(inv.id)}`;
  els.qrImage.textContent = t("qr.loading");
  els.qrModal.showModal();

  try {
    const res = await fetch(`/api/invoices/${encodeURIComponent(inv.id)}/qr`);
    if (!res.ok) throw new Error("qr");
    const data = await res.json();
    if (data.payment_url) els.qrLink.href = data.payment_url;
    let src = "";
    if (data.image) src = data.image.startsWith("data:") ? data.image : `data:image/png;base64,${data.image}`;
    else if (data.qr_image_url) src = data.qr_image_url;
    if (!src) throw new Error("qr");
    const img = el("img");
    img.alt = `${t("qr.title")} · ${inv.id}`;
    img.src = src;
    els.qrImage.replaceChildren(img);
  } catch (_) {
    els.qrImage.textContent = t("qr.failed");
  }
}

// ---------- speech ----------

function setupSpeech() {
  const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
  if (!SR) {
    els.mic.disabled = true;
    return;
  }
  recognition = new SR();
  recognition.continuous = false;
  recognition.interimResults = false;
  recognition.lang = localeInfo().speech_code || state.locale;

  recognition.addEventListener("result", (event) => {
    const transcript = event.results[0][0].transcript;
    if (transcript) sendMessage(transcript);
  });
  recognition.addEventListener("end", () => setMicState(false));
  recognition.addEventListener("error", (e) => {
    setMicState(false);
    if (e.error !== "aborted") setStatus(t("error.voice"), true);
  });
}

function toggleMic() {
  if (!recognition || state.busy) return;
  if (state.recognizing) {
    recognition.stop();
    setMicState(false);
    return;
  }
  recognition.lang = localeInfo().speech_code || state.locale;
  try {
    recognition.start();
    setMicState(true);
    setStatus("");
  } catch (_) {}
}

function setMicState(on) {
  state.recognizing = on;
  els.mic.setAttribute("aria-pressed", String(on));
  if (recognition) els.micLabel.textContent = t(on ? "mic.listening" : "mic.idle");
}

// ---------- conversation ----------

function renderSuggestions() {
  els.suggestions.innerHTML = "";
  for (const key of ["suggest.1", "suggest.2", "suggest.3"]) {
    const b = el("button", "suggestion", t(key));
    b.type = "button";
    b.addEventListener("click", () => sendMessage(t(key)));
    els.suggestions.appendChild(b);
  }
}

// ask sends a prompt from elsewhere in the panel (a grid action, the chase
// button) and brings the assistant into view on phones.
function ask(text) {
  openAssistant();
  sendMessage(text);
}

// expectsAnswer guesses whether the assistant asked a yes/no question, so the
// panel can offer big confirm/cancel buttons instead of making the merchant
// speak or type a reply. Thai questions often end in a particle, not "?".
function expectsAnswer(reply) {
  const s = String(reply || "").trim();
  return /[?？]$/.test(s) || /(ไหม|มั้ย|หรือไม่|หรือเปล่า|ใช่ไหม)[\s.]*$/.test(s) || /\?\s*\S{0,3}$/.test(s);
}

async function sendMessage(text) {
  if (state.busy) return;
  els.welcome.hidden = true;
  els.quickReplies.hidden = true;
  els.message.value = "";
  appendUser(text);
  const typing = appendTyping();
  setBusy(true);
  setStatus(t("thinking"));

  try {
    const res = await fetch("/api/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ message: text, locale: state.locale, history: state.history }),
    });
    if (res.status === 429) throw new Error(t("error.rate"));
    if (res.status === 503) throw new Error(t("error.unavailable"));
    if (!res.ok) throw new Error(t("error.generic"));
    const data = await res.json();
    const calls = data.tool_calls || [];

    // The model API rejects blank turns, so a turn that only proposes an
    // action still needs text.
    const proposed = data.pending ? data.pending.actions.map((a) => a.name).join(", ") : "";
    const turnText = data.reply || (proposed ? `(Proposed ${proposed}; waiting for the merchant to approve.)` : "…");
    state.history.push({ role: "user", text });
    // "text" goes back to the model; "reply" is what the merchant saw.
    state.history.push({ role: "assistant", text: turnText, reply: data.reply || "", tool_calls: calls });
    saveChat();

    // Refresh first so invoice cards in the reply show current data.
    if (calls.length) await refreshInvoices();
    typing.remove();
    const li = appendAssistant(data.reply, calls);
    if (data.pending) li.appendChild(confirmCard(data.pending));
    scrollConversation();
    logActivity(calls);
    setStatus("");
    els.quickReplies.hidden = Boolean(data.pending) || !expectsAnswer(data.reply);

    const show = calls.find((c) => c.name === "show_invoices" && !c.is_error);
    if (show) applyShowInvoices(show);
    const qr = calls.find((c) => c.name === "generate_invoice_qr_code" && !c.is_error);
    if (qr) {
      const id = invoiceIdOf(qr);
      const inv = state.invoices.find((i) => i.id === id) || { id };
      if (id) showInvoiceQR(inv);
    }
    if (state.ttsEnabled && data.reply) speak(data.reply, data.locale || state.locale);
  } catch (err) {
    typing.remove();
    // fetch throws TypeError on network failure; never show its raw text.
    appendError(err instanceof TypeError ? t("error.generic") : err.message || t("error.generic"));
    setStatus("");
  } finally {
    setBusy(false);
  }
}

async function speak(text, locale) {
  try {
    const res = await fetch("/api/speak", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text, locale }),
    });
    if (res.status === 204 || !res.ok) return;
    const url = URL.createObjectURL(await res.blob());
    const audio = new Audio(url);
    audio.addEventListener("ended", () => URL.revokeObjectURL(url));
    audio.play().catch(() => URL.revokeObjectURL(url));
  } catch (_) {}
}

function appendUser(text) {
  const li = el("li", "msg msg-user");
  li.appendChild(el("div", "bubble", text));
  els.messages.appendChild(li);
  scrollConversation();
}

function appendTyping() {
  const li = el("li", "msg msg-assistant");
  const bubble = el("div", "bubble typing");
  bubble.append(el("i"), el("i"), el("i"));
  li.appendChild(bubble);
  els.messages.appendChild(li);
  scrollConversation();
  return li;
}

function appendAssistant(reply, calls) {
  const li = el("li", "msg msg-assistant");
  const visible = calls.filter((c) => c.name !== "list_invoices" || c.is_error);
  if (visible.length) {
    const actions = el("div", "msg-actions");
    for (const c of visible) actions.appendChild(actionCard(c));
    li.appendChild(actions);
  }
  if (reply) li.appendChild(el("div", "bubble", reply));
  const found = invoicesIn(calls);
  if (found.length) li.appendChild(invoiceList(found));
  els.messages.appendChild(li);
  scrollConversation();
  return li;
}

function appendError(text) {
  const li = el("li", "msg msg-assistant msg-error");
  li.appendChild(el("div", "bubble", text));
  els.messages.appendChild(li);
  scrollConversation();
}

function scrollConversation() {
  els.conversation.scrollTop = els.conversation.scrollHeight;
}

// ---------- confirmation ----------

// proposalDetails reads the customer, line items and total from a proposed
// tool call. It accepts the flat mock shape and PayPal's nested invoice shape.
function proposalDetails(pending) {
  const input = parseJSON(pending.input) || {};
  if (input.invoice_id) {
    const inv = state.invoices.find((i) => i.id === input.invoice_id) || {};
    return { invoice: input.invoice_id, customer: inv.customer, total: money(inv.amount, inv.currency), items: [] };
  }
  const recipient = (input.primary_recipients || [])[0] || {};
  const customer = input.recipient_email || (recipient.billing_info || {}).email_address || "";
  let currency = input.currency_code || (input.detail || {}).currency_code || "";
  let sum = 0;
  const items = (input.items || []).map((it) => {
    const unit = typeof it.unit_amount === "object" && it.unit_amount ? it.unit_amount : { value: it.unit_amount };
    if (!currency && unit.currency_code) currency = unit.currency_code;
    const qty = Number.parseFloat(it.quantity || "1") || 1;
    const value = Number.parseFloat(unit.value) || 0;
    sum += qty * value;
    return { name: it.name || "", qty, value };
  });
  const total = input.total || (items.length ? String(sum) : "");
  return {
    customer,
    items: items.map((i) => ({ label: i.qty > 1 ? `${i.qty} × ${i.name}` : i.name, amount: money(i.value * i.qty, currency) })),
    total: total ? money(total, currency) : "",
  };
}

// confirmLabels picks the card's title and button text: specific when every
// action is the same kind ("Send 3 reminders"), generic when they are mixed.
function confirmLabels(actions) {
  const names = new Set(actions.map((a) => a.name));
  const n = actions.length;
  if (names.size > 1) return { title: t("confirm.mixed.title", { n }), go: t("confirm.mixed.go") };
  const name = actions[0].name;
  return n === 1
    ? { title: t(`confirm.${name}.title`), go: t(`confirm.${name}.go`) }
    : { title: t(`confirm.${name}.titleMany`, { n }), go: t(`confirm.${name}.goMany`, { n }) };
}

function confirmCard(pending) {
  const actions = pending.actions || [];
  const labels = confirmLabels(actions);
  const card = el("div", "confirm-card");
  card.appendChild(el("p", "confirm-title", labels.title));

  const dl = el("dl", "confirm-details");
  const row = (label, value, cls) => {
    if (!value) return;
    const div = el("div", cls);
    div.append(el("dt", "", label), el("dd", "", value));
    dl.appendChild(div);
  };
  if (actions.length === 1) {
    const d = proposalDetails(actions[0]);
    row(t("confirm.customer"), d.customer);
    if (d.invoice) row(t("confirm.invoice"), d.invoice, "mono-row");
    for (const it of d.items) row(it.label, it.amount);
    row(t("confirm.total"), d.total, "confirm-total");
  } else {
    // One line per action: who it goes to and how much.
    for (const action of actions) {
      const d = proposalDetails(action);
      row(d.customer || d.invoice || action.name, d.total || "–");
    }
  }
  card.appendChild(dl);
  card.appendChild(el("p", "confirm-note", t("confirm.note")));

  const buttons = el("div", "confirm-buttons");
  const yes = el("button", "btn btn-primary btn-lg");
  yes.type = "button";
  yes.append(icon("check"), document.createTextNode(labels.go));
  const no = el("button", "btn btn-ghost btn-lg", t("confirm.cancel"));
  no.type = "button";
  buttons.append(yes, no);
  card.appendChild(buttons);

  yes.addEventListener("click", () => approve(pending, card));
  no.addEventListener("click", () => decline(pending, card));
  return card;
}

async function approve(pending, card) {
  if (state.busy) return;
  setBusy(true);
  card.querySelectorAll("button").forEach((b) => (b.disabled = true));
  try {
    const res = await fetch("/api/confirm", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: pending.id }),
    });
    if (res.status === 404) throw new Error(t("confirm.expired"));
    if (!res.ok) throw new Error(t("error.generic"));
    const calls = (await res.json()).tool_calls || [];

    const last = state.history[state.history.length - 1];
    if (last && last.role === "assistant") last.tool_calls = [...(last.tool_calls || []), ...calls];
    saveChat();

    await refreshInvoices();
    const results = el("div", "msg-actions");
    for (const c of calls) results.appendChild(actionCard(c));
    card.replaceWith(results);
    logActivity(calls);
    const done = calls.find((c) => !c.is_error);
    if (state.ttsEnabled && done) speak(t(`tool.${done.name}`), state.locale);
  } catch (err) {
    const msg = err instanceof TypeError ? t("error.generic") : err.message || t("error.generic");
    card.replaceWith(el("div", "confirm-result error", msg));
  } finally {
    setBusy(false);
  }
}

// decline needs no server call: the unapproved action simply expires.
function decline(pending, card) {
  const last = state.history[state.history.length - 1];
  if (last && last.role === "assistant") {
    const names = (pending.actions || []).map((a) => a.name).join(", ");
    last.text = `${last.text}\n(The merchant declined: ${names}.)`;
    last.declined = true;
    saveChat();
  }
  card.replaceWith(el("div", "confirm-result", t("confirm.cancelled")));
}

// ---------- saved conversation ----------

// The conversation and activity log are kept in this browser only, so a
// refresh does not lose context. The server keeps no chat history.
const CHAT_KEY = "paysay.chat";
const ACTIVITY_KEY = "paysay.activity";
const MAX_SAVED_TURNS = 40; // 20 exchanges
const MAX_SAVED_ACTIVITY = 50;

function saveChat() {
  state.history = state.history.slice(-MAX_SAVED_TURNS);
  try {
    localStorage.setItem(CHAT_KEY, JSON.stringify(state.history));
  } catch (_) {}
}

function loadSaved(key) {
  try {
    const v = JSON.parse(localStorage.getItem(key) || "[]");
    return Array.isArray(v) ? v : [];
  } catch (_) {
    return [];
  }
}

function restoreConversation() {
  state.history = loadSaved(CHAT_KEY);
  for (const turn of state.history) {
    if (turn.role === "user") {
      appendUser(turn.text);
      continue;
    }
    // Older saved turns have no "reply"; their text is what was shown.
    const li = appendAssistant(turn.reply !== undefined ? turn.reply : turn.text, turn.tool_calls || []);
    if (turn.declined) li.appendChild(el("div", "confirm-result", t("confirm.cancelled")));
  }
  els.welcome.hidden = state.history.length > 0;

  const saved = loadSaved(ACTIVITY_KEY);
  for (const entry of saved.slice().reverse()) addActivity(entry.call, new Date(entry.at));
  els.activityEmpty.hidden = els.activity.children.length > 0;
}

function newConversation() {
  state.history = [];
  try {
    localStorage.removeItem(CHAT_KEY);
  } catch (_) {}
  els.messages.innerHTML = "";
  els.quickReplies.hidden = true;
  els.welcome.hidden = false;
  setStatus("");
}

// ---------- invoice cards ----------

const CHAT_INVOICE_LIMIT = 5;

// invoicesIn returns the invoices the agent looked at, using the dashboard's
// normalized rows so the cards match the grid exactly.
function invoicesIn(calls) {
  const ids = new Set();
  for (const c of calls) {
    if (c.is_error || (c.name !== "list_invoices" && c.name !== "get_invoice")) continue;
    for (const m of String(c.output || "").matchAll(/INV2-[A-Z0-9-]+/g)) ids.add(m[0]);
  }
  return state.invoices.filter((inv) => ids.has(inv.id));
}

function invoiceList(invoices) {
  const list = el("ul", "chat-invoices");
  // Label the cards as what the assistant looked at, so they never read as
  // contradicting a reply like "no unpaid invoices".
  list.appendChild(el("li", "chat-invoices-head", t(invoices.length === 1 ? "chat.checkedOne" : "chat.checked", { n: invoices.length })));
  for (const inv of invoices.slice(0, CHAT_INVOICE_LIMIT)) {
    const status = String(inv.status || "").toUpperCase();
    const li = el("li", "chat-invoice");
    const top = el("div", "chat-invoice-row");
    top.append(el("span", "chat-invoice-who", inv.customer || inv.id), el("span", "chat-invoice-amount", money(inv.amount, inv.currency)));
    const bottom = el("div", "chat-invoice-row");
    const meta = el("span", "chat-invoice-meta");
    meta.append(el("span", `pill pill-${status}`, t(`status.${status}`)), el("span", "", shortDate(inv.created)));
    bottom.append(meta, actionCell(inv));
    li.append(top, bottom);
    list.appendChild(li);
  }
  if (invoices.length > CHAT_INVOICE_LIMIT) {
    const more = el("button", "link-btn chat-invoice-more", t("chat.more", { n: invoices.length - CHAT_INVOICE_LIMIT }));
    more.type = "button";
    more.addEventListener("click", () => {
      els.assistant.classList.remove("open");
      showView("invoices");
    });
    list.appendChild(more);
  }
  return list;
}

// ---------- action cards ----------

function parseJSON(s) {
  if (s && typeof s === "object") return s;
  try {
    return JSON.parse(s);
  } catch (_) {
    return null;
  }
}

function invoiceIdOf(call) {
  const out = parseJSON(call.output) || {};
  const input = parseJSON(call.input) || {};
  const found = String(call.output || "").match(/INV2-[A-Z0-9-]+/);
  return out.id || out.invoice_id || input.invoice_id || (found ? found[0] : "");
}

function actionCard(call, when) {
  const card = el("div", `action-card${call.is_error ? " error" : ""}`);
  const ic = el("span", "action-icon");
  ic.appendChild(icon(call.is_error ? "alert" : "check"));

  const body = el("div", "action-body");
  body.appendChild(el("div", "action-title", call.is_error ? t("tool.failed") : t(`tool.${call.name}`)));

  const out = parseJSON(call.output) || {};
  const meta = el("div", "action-meta");
  if (call.name === "show_invoices") {
    const input = parseJSON(call.input) || {};
    const status = String(input.status || "ALL").toUpperCase();
    meta.appendChild(el("span", "", status === "OPEN" ? t("filter.open") : status === "ALL" ? t("filter.all") : t(`status.${status}`)));
    if (input.customer) meta.appendChild(el("span", "", `“${input.customer}”`));
  }
  const id = invoiceIdOf(call);
  if (id) meta.appendChild(el("span", "mono", id));
  // Results that only carry an ID get the customer and amount from the table.
  const known = id ? state.invoices.find((i) => i.id === id) : null;
  const who = out.recipient || (parseJSON(call.input) || {}).recipient_email || (known && known.customer);
  if (who) meta.appendChild(el("span", "", who));
  if (out.total) meta.appendChild(el("span", "", money(out.total, out.currency_code)));
  else if (known) meta.appendChild(el("span", "", money(known.amount, known.currency)));
  if (when) meta.appendChild(el("span", "action-time", timeOfDay(when)));
  if (meta.childNodes.length) body.appendChild(meta);

  const details = el("details");
  details.appendChild(el("summary", "", `${t("details")} · ${call.name}`));
  const pretty = parseJSON(call.output);
  details.appendChild(el("pre", "", pretty ? JSON.stringify(pretty, null, 2) : String(call.output || "")));
  body.appendChild(details);

  card.append(ic, body);
  return card;
}

function logActivity(calls) {
  if (calls.length === 0) return;
  const now = new Date();
  const saved = loadSaved(ACTIVITY_KEY);
  for (const c of calls) {
    addActivity(c, now);
    saved.push({ call: c, at: now.toISOString() });
  }
  try {
    localStorage.setItem(ACTIVITY_KEY, JSON.stringify(saved.slice(-MAX_SAVED_ACTIVITY)));
  } catch (_) {}
  els.activityEmpty.hidden = true;
}

function addActivity(call, when) {
  const li = el("li");
  li.appendChild(actionCard(call, when));
  els.activity.prepend(li);
}

// ---------- status ----------

function setStatus(msg, isError) {
  els.status.textContent = msg || "";
  els.status.classList.toggle("error", Boolean(isError));
}

function setBusy(busy) {
  state.busy = busy;
  els.send.disabled = busy;
  els.message.disabled = busy;
  els.mic.disabled = busy || !recognition;
  els.chase.disabled = busy;
}

init();
