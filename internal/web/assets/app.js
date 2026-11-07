// PaySay demo front-end.
//
// Speech-to-text uses the browser Web Speech API (SpeechRecognition), which
// supports the demo locales. Replies are played back via the server's /api/speak
// endpoint (Amazon Polly) when a voice is available for the locale, otherwise
// the reply is shown as text only. All PayPal tool calls the agent makes are
// listed in the activity panel for transparency.

"use strict";

const state = {
  locale: "en-US",
  ttsEnabled: true,
  history: [],
  recognizing: false,
};

const els = {
  brand: document.getElementById("brand"),
  tagline: document.getElementById("tagline"),
  modeBadge: document.getElementById("mode-badge"),
  locale: document.getElementById("locale"),
  messages: document.getElementById("messages"),
  composer: document.getElementById("composer"),
  message: document.getElementById("message"),
  mic: document.getElementById("mic"),
  send: document.getElementById("send"),
  status: document.getElementById("status"),
  toolCalls: document.getElementById("tool-calls"),
  invoiceGrid: document.getElementById("invoice-grid"),
  statusFilter: document.getElementById("status-filter"),
  refresh: document.getElementById("refresh"),
};

let gridApi = null;

// --- initialization ---

async function init() {
  await loadConfig();
  await loadLocales();
  wireEvents();
  setupSpeech();
  setupGrid();
  refreshInvoices();
}

async function loadConfig() {
  try {
    const res = await fetch("/api/config");
    const cfg = await res.json();
    els.brand.textContent = cfg.brand;
    els.tagline.textContent = cfg.tagline;
    document.title = `${cfg.brand} — ${cfg.tagline}`;
    state.ttsEnabled = Boolean(cfg.tts_enabled);
    state.locale = cfg.default_locale || "en-US";
    if (cfg.live_mode) {
      els.modeBadge.textContent = "Live PayPal sandbox";
      els.modeBadge.classList.add("live");
    } else {
      els.modeBadge.textContent = "Demo mode (simulated PayPal, no real invoices)";
      els.modeBadge.classList.add("mock");
    }
  } catch (err) {
    setStatus("Could not load configuration.", true);
  }
}

async function loadLocales() {
  try {
    const res = await fetch("/api/locales");
    const data = await res.json();
    els.locale.innerHTML = "";
    for (const l of data.locales) {
      const opt = document.createElement("option");
      opt.value = l.tag;
      opt.textContent = `${l.native_name} (${l.english_name})`;
      opt.dataset.speechCode = l.speech_code;
      opt.dataset.supportsTts = String(l.supports_tts);
      els.locale.appendChild(opt);
    }
    els.locale.value = state.locale;
  } catch (err) {
    setStatus("Could not load languages.", true);
  }
}

function wireEvents() {
  els.locale.addEventListener("change", () => {
    state.locale = els.locale.value;
    if (recognition) recognition.lang = selectedSpeechCode();
  });
  els.composer.addEventListener("submit", (e) => {
    e.preventDefault();
    const text = els.message.value.trim();
    if (text) sendMessage(text);
  });
  els.mic.addEventListener("click", toggleMic);
  els.refresh.addEventListener("click", refreshInvoices);
  els.statusFilter.addEventListener("change", refreshInvoices);
}

// --- invoice grid (AG Grid) ---

function setupGrid() {
  if (!window.agGrid || !els.invoiceGrid) return;
  // Register all community modules (required by AG Grid v33+).
  if (agGrid.ModuleRegistry && agGrid.AllCommunityModule) {
    agGrid.ModuleRegistry.registerModules([agGrid.AllCommunityModule]);
  }

  const currencyAmount = (p) => {
    if (!p.data) return "";
    const amt = p.data.amount || "";
    const cur = p.data.currency || "";
    return amt ? `${amt} ${cur}`.trim() : "";
  };

  const gridOptions = {
    theme: agGrid.themeQuartz ? agGrid.themeQuartz.withParams({
      backgroundColor: "#151b36",
      foregroundColor: "#f4f6ff",
      headerBackgroundColor: "#1d2547",
      borderColor: "#333c66",
      accentColor: "#2b8aef",
    }) : "legacy",
    columnDefs: [
      { field: "id", headerName: "Invoice", minWidth: 130, flex: 1 },
      { field: "customer", headerName: "Customer", minWidth: 150, flex: 1.3 },
      { headerName: "Amount", valueGetter: currencyAmount, minWidth: 110, flex: 1 },
      {
        field: "status",
        headerName: "Status",
        minWidth: 110,
        flex: 1,
        cellClass: (p) => "status-" + String(p.value || "").toLowerCase(),
      },
    ],
    defaultColDef: { sortable: true, filter: true, resizable: true },
    rowData: [],
    // Accessibility: AG Grid renders an ARIA grid; provide an overlay message.
    overlayNoRowsTemplate:
      '<span class="grid-empty">No invoices yet. Create one by voice to see it here.</span>',
    domLayout: "autoHeight",
  };

  gridApi = agGrid.createGrid(els.invoiceGrid, gridOptions);
}

async function refreshInvoices() {
  if (!gridApi) return;
  const status = els.statusFilter ? els.statusFilter.value : "";
  const qs = status ? `?status=${encodeURIComponent(status)}` : "";
  try {
    const res = await fetch(`/api/invoices${qs}`);
    if (!res.ok) return;
    const data = await res.json();
    gridApi.setGridOption("rowData", data.invoices || []);
  } catch (_) {
    // Non-fatal: the grid simply keeps its current rows.
  }
}

// --- speech recognition ---

let recognition = null;

function setupSpeech() {
  const SR = window.SpeechRecognition || window.webkitSpeechRecognition;
  if (!SR) {
    els.mic.disabled = true;
    els.mic.setAttribute("aria-label", "Voice input not supported in this browser");
    els.mic.title = "Voice input is not supported in this browser; please type.";
    return;
  }
  recognition = new SR();
  recognition.continuous = false;
  recognition.interimResults = false;
  recognition.lang = selectedSpeechCode();

  recognition.addEventListener("result", (event) => {
    const transcript = event.results[0][0].transcript;
    els.message.value = transcript;
    sendMessage(transcript);
  });
  recognition.addEventListener("end", () => setMicState(false));
  recognition.addEventListener("error", (e) => {
    setMicState(false);
    setStatus(`Voice input error: ${e.error}`, true);
  });
}

function selectedSpeechCode() {
  const opt = els.locale.selectedOptions[0];
  return (opt && opt.dataset.speechCode) || state.locale;
}

function toggleMic() {
  if (!recognition) return;
  if (state.recognizing) {
    recognition.stop();
    setMicState(false);
    return;
  }
  recognition.lang = selectedSpeechCode();
  try {
    recognition.start();
    setMicState(true);
    setStatus("Listening…");
  } catch (_) {
    // start() throws if already started; ignore.
  }
}

function setMicState(on) {
  state.recognizing = on;
  els.mic.setAttribute("aria-pressed", String(on));
  els.mic.setAttribute("aria-label", on ? "Stop voice input" : "Start voice input");
  if (!on && els.status.textContent === "Listening…") setStatus("");
}

// --- messaging ---

async function sendMessage(text) {
  appendMessage("user", text);
  els.message.value = "";
  setBusy(true);
  setStatus("Thinking…");

  try {
    const res = await fetch("/api/chat", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        message: text,
        locale: state.locale,
        history: state.history,
      }),
    });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.error || `request failed (${res.status})`);
    }
    const data = await res.json();
    state.history.push({ role: "user", text });
    state.history.push({ role: "assistant", text: data.reply });
    appendMessage("assistant", data.reply);
    renderToolCalls(data.tool_calls || []);
    setStatus("");
    // The agent may have created, sent, or modified invoices; refresh the grid.
    if ((data.tool_calls || []).length > 0) refreshInvoices();
    if (state.ttsEnabled && data.reply) speak(data.reply, data.locale || state.locale);
  } catch (err) {
    appendMessage("assistant", "Sorry, something went wrong.");
    setStatus(err.message, true);
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
    if (res.status === 204 || !res.ok) return; // no voice for this locale
    const blob = await res.blob();
    const url = URL.createObjectURL(blob);
    const audio = new Audio(url);
    audio.addEventListener("ended", () => URL.revokeObjectURL(url));
    audio.play().catch(() => URL.revokeObjectURL(url));
  } catch (_) {
    // TTS is best-effort; ignore failures.
  }
}

// --- rendering ---

function appendMessage(role, text) {
  const li = document.createElement("li");
  li.className = role;
  const label = document.createElement("span");
  label.className = "role";
  label.textContent = role === "user" ? "You" : "PaySay";
  const body = document.createElement("span");
  body.textContent = text;
  li.appendChild(label);
  li.appendChild(body);
  els.messages.appendChild(li);
  els.messages.scrollTop = els.messages.scrollHeight;
}

function renderToolCalls(calls) {
  for (const c of calls) {
    const li = document.createElement("li");
    if (c.is_error) li.classList.add("error");
    const name = document.createElement("div");
    name.className = "tool-name";
    name.textContent = c.is_error ? `⚠ ${c.name}` : `✓ ${c.name}`;
    li.appendChild(name);
    const pre = document.createElement("pre");
    pre.textContent = prettify(c.output);
    li.appendChild(pre);
    els.toolCalls.appendChild(li);
  }
}

function prettify(s) {
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch (_) {
    return s;
  }
}

function setStatus(msg, isError) {
  els.status.textContent = msg || "";
  els.status.classList.toggle("error", Boolean(isError));
}

function setBusy(busy) {
  els.send.disabled = busy;
  els.message.disabled = busy;
}

init();
