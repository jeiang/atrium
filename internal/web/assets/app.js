const THEME_COOKIE = "atrium_theme";
const RAIL_COOKIE = "atrium_rail";
const systemDark = window.matchMedia("(prefers-color-scheme: dark)");

const setCookie = (name, value) => {
  const secure = location.protocol === "https:" ? "; Secure" : "";
  document.cookie = `${name}=${value}; Path=/; Max-Age=31536000; SameSite=Lax${secure}`;
};

const applyTheme = () => {
  const root = document.documentElement;
  const theme = root.dataset.theme || "system";
  root.classList.toggle("dark", theme === "dark" || (theme === "system" && systemDark.matches));
};
applyTheme();
systemDark.addEventListener("change", applyTheme);

const sidebarOpen = (open) => {
  const sidebar = document.getElementById("sidebar");
  if (!sidebar) return;
  sidebar.toggleAttribute("data-open", open);
  document.getElementById("sidebar-backdrop")?.toggleAttribute("data-open", open);
  const toggle = document.getElementById("nav-toggle");
  toggle?.setAttribute("aria-expanded", String(open));
  if (open) sidebar.querySelector("a, button")?.focus();
  else toggle?.focus();
};

window.matchMedia("(min-width: 768px)").addEventListener("change", (event) => {
  if (event.matches && document.getElementById("sidebar")?.hasAttribute("data-open")) sidebarOpen(false);
});

document.addEventListener("htmx:configRequest", (event) => {
  if (event.detail.verb !== "get") return;
  const data = event.detail.formData;
  [...data.keys()].forEach((key) => {
    if (data.getAll(key).every((value) => value === "")) data.delete(key);
  });
});

document.addEventListener("htmx:historyRestore", () => {
  connectAll();
  const params = new URLSearchParams(window.location.search);
  document.querySelectorAll("form:is([hx-push-url], [hx-replace-url]) [name]").forEach((field) => {
    if (field.type === "checkbox") field.checked = params.has(field.name);
    else if (field.type === "radio") field.checked = field.value === (params.get(field.name) ?? "");
    else if (field.type !== "hidden") field.value = params.get(field.name) ?? "";
  });
  window.basecoat.initAll({ force: true });
});

const dialogOpeners = new WeakMap();
let panelOpener = null;
const panelInert = new Map();

const loadingForm = () => {
  const status = document.createElement("div");
  status.setAttribute("role", "status");
  status.setAttribute("aria-label", "Loading form");
  status.className = "flex flex-col gap-3";
  ["skeleton h-6 w-3/4 rounded-md bg-muted", "skeleton h-4 w-1/2 rounded-md bg-muted", "skeleton h-10 w-full rounded-md bg-muted"].forEach((className) => {
    const bar = document.createElement("div");
    bar.className = className;
    status.appendChild(bar);
  });
  return status;
};

const focusDialog = (dialog) => {
  dialog.removeAttribute("aria-busy");
  const focusTarget = dialog.querySelector("[autofocus]") ||
    dialog.querySelector('[role="alert"][tabindex="-1"]') ||
    dialog.querySelector("form input:not([type=hidden]), form textarea, form select") ||
    dialog.querySelector("h2");
  if (!focusTarget) return;
  if (focusTarget.tagName === "H2") focusTarget.setAttribute("tabindex", "-1");
  focusTarget.focus();
};

document.addEventListener("htmx:beforeRequest", (event) => {
  const target = event.detail.target;
  const loading = target && target.dataset.loading && document.getElementById(target.dataset.loading);
  if (loading) target.replaceChildren(loading.content.cloneNode(true));
  const dialog = target && target.closest("dialog");
  if (dialog) {
    const opener = event.detail.elt;
    if (!dialog.open) dialog.showModal();
    if (opener && opener.id) dialogOpeners.set(dialog, opener.id);
    dialog.setAttribute("aria-busy", "true");
    if (target === dialog && !dialog.contains(opener)) dialog.replaceChildren(loadingForm());
  } else if (event.detail.boosted) {
    document.querySelectorAll("dialog[open]").forEach((openDialog) => openDialog.close());
  }
});

document.addEventListener("htmx:afterSwap", (event) => {
  const target = event.detail.target || event.target;
  if (target && target.id === "conditions") htmx.trigger("#rule-form", "change");
  const dialog = target && (target.matches("dialog") ? target : target.closest("dialog"));
  if (dialog) focusDialog(dialog);
});

document.addEventListener("close", (event) => {
  const dialog = event.target;
  if (!dialog.matches || !dialog.matches("dialog")) return;
  dialog.removeAttribute("aria-busy");
  const opener = dialogOpeners.get(dialog);
  dialogOpeners.delete(dialog);
  if (!opener) return;
  const openerTarget = document.getElementById(opener);
  if (openerTarget) {
    openerTarget.focus();
    return;
  }
  if (document.activeElement !== document.body) return;
  const fallback = document.querySelector("[data-focus-fallback]") || document.getElementById("content");
  if (fallback) fallback.focus();
}, true);

document.addEventListener("close-dialogs", () => {
  document.querySelectorAll("dialog[open]").forEach((dialog) => dialog.close());
});

const swapStatuses = new Set([403, 404, 409, 413, 422, 429]);
document.addEventListener("htmx:beforeSwap", (event) => {
  const xhr = event.detail.xhr;
  const html = (xhr.getResponseHeader("content-type") || "").startsWith("text/html");
  if (html && swapStatuses.has(xhr.status)) {
    event.detail.shouldSwap = true;
    event.detail.isError = false;
  }
});
const focusFallback = (target) => {
  if (document.activeElement !== document.body || !target) return;
  const fallback = target.matches("[data-focus-fallback]") ? target : target.querySelector("[data-focus-fallback]");
  if (fallback) fallback.focus();
};


document.addEventListener("htmx:afterRequest", (event) => {
  const form = event.detail.elt;
  const status = event.detail.xhr.status;
  const succeeded = status >= 200 && status < 400;
  if (form.matches && form.matches("form[data-reset]") && succeeded) form.reset();
  if (form.id === "composer") {
    setTimeout(() => {
      if (document.activeElement === document.body) document.getElementById("composer-text")?.focus();
    });
  }
  if (form.matches && form.matches("button[data-suggestion]") && succeeded) {
    const item = form.closest("li");
    const next = item.nextElementSibling || item.previousElementSibling;
    item.remove();
    (next ? next.querySelector("button") : document.getElementById("add-condition")).focus();
  }
});

document.addEventListener("submit", (event) => {
  const box = event.target.querySelector?.("input[data-confirm-private]:checked");
  if (box) {
    const source = document.getElementById(box.dataset.confirmPrivate);
    const stored = (box.dataset.confirmDestination || "").trim();
    const destination = (source?.value || stored).trim();
    if (destination && destination === stored && !window.confirm(`Include private records in the export to ${destination}?`)) event.preventDefault();
  }
  const question = event.submitter?.dataset.confirmSubmit ?? event.target.dataset.confirmSubmit;
  if (question && !window.confirm(question)) event.preventDefault();
});

const refusal = (xhr) => {
  if (!xhr || !(xhr.getResponseHeader("content-type") || "").startsWith("application/json")) return "";
  try {
    const message = JSON.parse(xhr.responseText).error?.message;
    return typeof message === "string" ? message : "";
  } catch {
    return "";
  }
};

const failed = (title, description) => (event) => {
  const target = event.detail.target;
  if (target && target.dataset.loading) {
    target.replaceChildren();
    const dialog = target.closest("dialog");
    if (dialog) dialog.close();
  }
  document.getElementById("toaster").toast({ category: "error", title, description: refusal(event.detail.xhr) || description });
};
document.addEventListener("htmx:responseError", failed("Something went wrong", "atrium could not finish that request. Try again, or reload the page."));
document.addEventListener("htmx:sendError", failed("atrium is unreachable", "Check the connection to the server and try again."));

document.addEventListener("keydown", (event) => {
  if (event.ctrlKey || event.metaKey || event.altKey || event.target.closest("input, textarea, select, [contenteditable]")) return;
  const card = event.target.closest("[data-card]");
  if (!card) return;
  if (event.key === "Escape") {
    card.querySelectorAll("details[open]").forEach((details) => { details.open = false; });
    event.target.blur();
    return;
  }
  const action = { a: "[data-card-approve]", d: "[data-card-dismiss]" }[event.key.toLowerCase()];
  const button = action && card.querySelector(action);
  if (button && !button.disabled) {
    event.preventDefault();
    button.click();
  }
});

document.addEventListener("keydown", (event) => {
  if (event.key === "/" && !event.target.closest("input, textarea, select, [contenteditable]")) {
    const search = document.querySelector("[data-search-input]");
    if (search) {
      event.preventDefault();
      search.focus();
    }
  }
  if (event.key === "Enter" && (event.metaKey || event.ctrlKey) && event.target.matches("textarea[data-submit-on-enter]")) {
    event.preventDefault();
    event.target.form.requestSubmit();
  }
  if (event.key === "Escape" && document.getElementById("sidebar")?.hasAttribute("data-open")) sidebarOpen(false);
});

const actions = {
  "set-theme": (button) => {
    const theme = button.dataset.themeValue;
    document.documentElement.dataset.theme = theme;
    document.querySelector('meta[name="color-scheme"]')?.setAttribute("content", theme === "system" ? "light dark" : theme);
    document.querySelectorAll('[data-action="set-theme"]').forEach((other) => other.setAttribute("aria-pressed", String(other === button)));
    setCookie(THEME_COOKIE, theme);
    applyTheme();
  },
  "toggle-rail": (button) => {
    const sidebar = document.getElementById("sidebar");
    const rail = sidebar.dataset.rail !== "true";
    const label = rail ? "Expand sidebar" : "Collapse sidebar";
    sidebar.dataset.rail = String(rail);
    button.setAttribute("aria-expanded", String(!rail));
    button.setAttribute("aria-label", label);
    button.title = label;
    setCookie(RAIL_COOKIE, rail ? "1" : "0");
  },
  "open-sidebar": () => sidebarOpen(true),
  "close-sidebar": () => sidebarOpen(false),
  "close-dialog": (button) => button.closest("dialog")?.close(),
  "copy-value": async (button) => {
    const target = button.dataset.target ? document.getElementById(button.dataset.target) : null;
    const text = button.dataset.copy ?? target?.textContent ?? "";
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      if (!target) return;
      const range = document.createRange();
      range.selectNodeContents(target);
      const selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange(range);
      document.execCommand("copy");
      selection.removeAllRanges();
    }
  },
  "remove-condition": (button) => {
    const form = button.form;
    const row = button.closest("li");
    const next = row.nextElementSibling || row.previousElementSibling;
    row.remove();
    (next ? next.querySelector("select") : document.getElementById("add-condition")).focus();
    htmx.trigger(form, "change");
  },
  "cancel-correct": (button) => {
    const opener = button.closest("[id^=decision]").querySelector('[hx-get*="/correct?"]');
    button.closest("form").remove();
    opener.focus();
  },
};

document.addEventListener("click", (event) => {
  const button = event.target.closest("[data-action]");
  if (!button) return;
  const action = actions[button.dataset.action];
  if (action) action(button);
});

document.addEventListener("change", (event) => {
  if (event.target.matches("[data-submit-on-change]")) {
    event.target.form.submit();
    return;
  }
  const group = event.target.closest("form[data-selection] [data-group]");
  if (!group) return;
  const boxes = [...group.querySelectorAll("input[data-message]")];
  const toggle = group.querySelector("[data-group-toggle]");
  if (event.target === toggle) boxes.forEach((box) => { box.checked = toggle.checked; });
  const picked = boxes.filter((box) => box.checked).length;
  toggle.checked = picked > 0;
  toggle.indeterminate = picked > 0 && picked < boxes.length;
  group.querySelector("[data-picked]").textContent = picked;
  const form = group.closest("form");
  const total = form.querySelectorAll("input[data-message]:checked").length;
  const button = form.querySelector("[data-approve-picked]");
  const none = button.querySelector("[data-none]");
  form.querySelector("[data-picked-total]").textContent = total;
  button.querySelector("[data-some]").hidden = total === 0 && none !== null;
  if (none) none.hidden = total > 0;
  button.disabled = total === 0 && none === null;
});

document.addEventListener("htmx:confirm", (event) => {
  const submitter = event.detail.triggeringEvent && event.detail.triggeringEvent.submitter;
  if (!submitter || !submitter.dataset.confirm) return;
  const none = submitter.querySelector("[data-none]");
  const question = none && !none.hidden ? submitter.dataset.confirmNone : submitter.dataset.confirm;
  event.preventDefault();
  if (window.confirm(question)) event.detail.issueRequest(true);
});

document.addEventListener("DOMContentLoaded", () => {
  const strip = document.body.dataset.stripParams;
  if (!strip) return;
  const url = new URL(location.href);
  strip.split(",").forEach((name) => url.searchParams.delete(name));
  history.replaceState(history.state, "", url);
});

const torrentForm = () => document.querySelector("form[data-torrent-form]");

const setTorrent = (form, b64, name) => {
  form.elements.torrent.value = b64;
  form.elements.name.value = name;
  form.querySelector("[data-torrent-chosen]").textContent = name ? `${name} ready to add` : "";
};

const base64url = (buffer) => {
  let binary = "";
  new Uint8Array(buffer).forEach((byte) => { binary += String.fromCharCode(byte); });
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
};

document.addEventListener("DOMContentLoaded", () => {
  const form = torrentForm();
  if (!form || !location.hash.startsWith("#torrent=")) return;
  const params = new URLSearchParams(location.hash.slice(1));
  setTorrent(form, params.get("torrent") || "", params.get("name") || "upload.torrent");
  history.replaceState(history.state, "", location.pathname + location.search);
});

document.addEventListener("change", (event) => {
  if (!event.target.matches("[data-torrent-file]")) return;
  const form = event.target.form || torrentForm();
  const file = event.target.files[0];
  if (!file) { setTorrent(form, "", ""); return; }
  file.arrayBuffer().then((buffer) => setTorrent(form, base64url(buffer), file.name));
});

const pendingFiles = new WeakMap();
const uploadInput = (form) => form.querySelector("[data-upload]");
const formatBytes = (n) => (n < 1024 ? `${n} B` : n < 1048576 ? `${(n / 1024).toFixed(0)} KB` : `${(n / 1048576).toFixed(1)} MB`);

const renderChips = (form) => {
  const list = form.querySelector("[data-chips]");
  const files = [...(pendingFiles.get(form)?.files || [])];
  list.replaceChildren(...files.map((file, index) => {
    const chip = document.createElement("li");
    chip.className = "badge gap-1 max-w-64";
    chip.dataset.variant = "secondary";
    chip.innerHTML = `<span class="truncate"></span><span class="text-muted-foreground tabular-nums"></span><button type="button" class="rounded-sm hover:text-destructive" data-action="remove-file" data-index="${index}" aria-label="Remove ${escapeHtml(file.name)}"><span aria-hidden="true">×</span></button>`;
    chip.children[0].textContent = file.name;
    chip.children[1].textContent = formatBytes(file.size);
    return chip;
  }));
};

const setFiles = (form, files) => {
  const transfer = new DataTransfer();
  files.forEach((file) => transfer.items.add(file));
  pendingFiles.set(form, transfer);
  uploadInput(form).files = transfer.files;
  renderChips(form);
};

const addFiles = (form, files) => setFiles(form, [...(pendingFiles.get(form)?.files || []), ...files]);

actions["pick-files"] = (button) => uploadInput(button.form).click();
actions["remove-file"] = (button) => {
  const form = button.form;
  setFiles(form, [...pendingFiles.get(form).files].filter((_, i) => i !== Number(button.dataset.index)));
};

document.addEventListener("change", (event) => {
  if (event.target.matches("form[data-attach] [data-upload]")) addFiles(event.target.form, [...event.target.files]);
});
document.addEventListener("reset", (event) => {
  const form = event.target;
  if (!form.matches || !form.matches("form[data-attach]")) return;
  pendingFiles.delete(form);
  form.querySelector("[data-chips]").replaceChildren();
  form.querySelectorAll("select[data-option] option").forEach((option) => { option.defaultSelected = option.selected; });
  form.querySelectorAll("input[type=checkbox][hx-post]").forEach((box) => { box.defaultChecked = box.checked; });
});
["dragenter", "dragover"].forEach((name) => document.addEventListener(name, (event) => {
  const form = event.target.closest && event.target.closest("form[data-attach]");
  if (!form || ![...event.dataTransfer.types].includes("Files")) return;
  event.preventDefault();
  form.classList.add("ring-2", "ring-ring");
}));
document.addEventListener("dragleave", (event) => {
  const form = event.target.closest && event.target.closest("form[data-attach]");
  if (form && !form.contains(event.relatedTarget)) form.classList.remove("ring-2", "ring-ring");
});
document.addEventListener("drop", (event) => {
  const form = event.target.closest && event.target.closest("form[data-attach]");
  if (!form) return;
  event.preventDefault();
  form.classList.remove("ring-2", "ring-ring");
  if (event.dataTransfer.files.length) addFiles(form, [...event.dataTransfer.files]);
});

const PANEL_WIDTH_KEY = "atrium.panel.width";
const PANEL_MIN = 260;

const workspace = () => document.getElementById("workspace");
const panelOpen = (open, opener = document.activeElement, restoreFocus = true) => {
  const panel = document.getElementById("panel");
  if (!panel) return;
  const mobile = window.innerWidth < 768;
  if (open && mobile && !panel.hasAttribute("data-open")) panelOpener = opener;
  panel.toggleAttribute("data-open", open);
  document.getElementById("panel-backdrop")?.classList.toggle("hidden", !open);
  document.querySelectorAll('[data-action="toggle-panel"][aria-controls]').forEach((button) => button.setAttribute("aria-expanded", String(open)));
  if (open && mobile) {
    panel.setAttribute("role", "dialog");
    panel.setAttribute("aria-modal", "true");
    ["#thread", "#panel-handle", ".topbar", "#sidebar"].forEach((selector) => {
      const target = document.querySelector(selector);
      if (!target) return;
      if (!panelInert.has(target)) panelInert.set(target, target.hasAttribute("inert"));
      target.setAttribute("inert", "");
    });
    panel.querySelector('[role="tab"][aria-selected="true"]')?.focus();
  } else {
    panel.removeAttribute("role");
    panel.removeAttribute("aria-modal");
    panelInert.forEach((wasInert, target) => {
      if (wasInert) target.setAttribute("inert", "");
      else target.removeAttribute("inert");
    });
    panelInert.clear();
    const returnTo = panelOpener?.isConnected ? panelOpener : document.getElementById("panel-toggle");
    panelOpener = null;
    if (restoreFocus && returnTo) returnTo.focus();
  }
};

actions["toggle-panel"] = (button) => {
  const panel = document.getElementById("panel");
  if (panel) panelOpen(!panel.hasAttribute("data-open"), button);
};
actions["edit-message"] = (button) => {
  const form = document.getElementById("composer");
  form.elements.text.value = button.closest("li").querySelector("[data-text]").textContent;
  form.elements.edit.value = "1";
  document.getElementById("composer-editing").hidden = false;
  form.elements.text.focus();
};
actions["cancel-edit"] = () => {
  const form = document.getElementById("composer");
  form.elements.edit.value = "";
  form.elements.text.value = "";
  document.getElementById("composer-editing").hidden = true;
};

const showPreview = (opener) => {
  const tab = document.getElementById("panel-tab-preview");
  if (!tab) return;
  if (tab.getAttribute("aria-selected") !== "true") tab.click();
  if (window.innerWidth < 768) panelOpen(true, opener);
};

document.addEventListener("htmx:beforeRequest", (event) => {
  const target = event.detail.target;
  if (target && target.id === "preview-body") showPreview(event.detail.elt);
});

window.matchMedia("(min-width: 768px)").addEventListener("change", (event) => {
  const panel = document.getElementById("panel");
  if (event.matches && panel?.hasAttribute("data-open")) panelOpen(false, null, false);
});

document.addEventListener("DOMContentLoaded", () => {
  const root = workspace();
  if (!root) return;
  const stored = Number(localStorage.getItem(PANEL_WIDTH_KEY));
  if (stored >= PANEL_MIN) root.style.setProperty("--panel-w", `${stored}px`);
  const handle = document.getElementById("panel-handle");
  if (!handle) return;
  const panel = document.getElementById("panel");
  const measuredWidth = () => panel?.getBoundingClientRect().width || stored || 320;
  const updateAria = (width) => {
    const max = Math.floor(root.getBoundingClientRect().width * 0.6);
    const clamped = Math.min(Math.max(width, PANEL_MIN), max);
    handle.setAttribute("aria-valuemax", String(max));
    handle.setAttribute("aria-valuenow", String(clamped));
    handle.setAttribute("aria-valuetext", `${clamped} pixels`);
  };
  const apply = (width) => {
    const max = Math.floor(root.getBoundingClientRect().width * 0.6);
    const clamped = Math.min(Math.max(width, PANEL_MIN), max);
    root.style.setProperty("--panel-w", `${clamped}px`);
    updateAria(clamped);
    return clamped;
  };
  updateAria(measuredWidth());
  handle.addEventListener("pointerdown", (event) => {
    event.preventDefault();
    handle.setPointerCapture(event.pointerId);
    const right = root.getBoundingClientRect().right;
    let width = measuredWidth();
    const move = (e) => { width = apply(right - e.clientX); };
    const stop = () => {
      handle.removeEventListener("pointermove", move);
      localStorage.setItem(PANEL_WIDTH_KEY, String(Math.round(width)));
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", stop, { once: true });
    handle.addEventListener("pointercancel", stop, { once: true });
  });
  handle.addEventListener("keydown", (event) => {
    const delta = { ArrowLeft: 24, ArrowRight: -24 }[event.key];
    if (!delta) return;
    event.preventDefault();
    const width = apply(measuredWidth() + delta);
    localStorage.setItem(PANEL_WIDTH_KEY, String(Math.round(width)));
  });
});

document.addEventListener("keydown", (event) => {
  const panel = document.getElementById("panel");
  if (event.key === "Escape" && panel && panel.hasAttribute("data-open")) panelOpen(false);
});

const setBusy = (busy) => {
  const announcer = document.getElementById("transcript-announcer");
  if (announcer) announcer.textContent = busy ? "Working. Stop is available." : "";
  const form = document.getElementById("composer");
  if (!form) return;
  form.querySelector("[data-stop]").hidden = !busy;
  form.querySelector("[data-send]").hidden = busy;
  form.querySelector("[data-queue]").hidden = !busy;
};

const refreshWork = () => {
  const work = document.getElementById("panel-work");
  if (work) htmx.trigger(work, "work-changed");
};

const escapeHtml = (text) => text.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);

const toolVerb = (name) => {
  const words = String(name).split(/[_\-.\s]+/).filter(Boolean);
  if (!words.length) return String(name);
  words[0] = words[0][0].toUpperCase() + words[0].slice(1);
  return words.join(" ");
};

const fromTemplate = (template) => template.content.firstElementChild.cloneNode(true);

const userMessage = (template, text) => {
  const bubble = fromTemplate(template);
  bubble.querySelector("[data-user-text]").textContent = text;
  return bubble;
};

const liveRuns = new Map();
const refusedRuns = new Map();
const watchedRuns = new WeakSet();
const SLOW_RETRY_MS = 30000;
const checkpointKey = (thread) => `atrium.stream.${thread}`;
const readCheckpoint = (thread) => {
  try {
    return JSON.parse(sessionStorage.getItem(checkpointKey(thread))) || {};
  } catch {
    return {};
  }
};

const connectLive = (li) => {
  const runId = Number(li.dataset.liveRun);
  const current = liveRuns.get(runId);
  if (current && current.li === li) return;
  if (current) current.source.close();
  const thread = li.dataset.thread;
  const label = li.querySelector("[data-live-label]");
  const answer = li.querySelector("[data-live-answer]");
  const answerText = li.querySelector("[data-live-text]");
  const mainSteps = li.querySelector("[data-live-steps]");
  const groups = new Map([[runId, mainSteps]]);
  const userTemplate = li.querySelector("template[data-message=user]");

  const stored = readCheckpoint(thread);
  const resume = stored.run === runId && li.dataset.streamCheckpoint === String(stored.lastId);
  if (resume) {
    li.querySelectorAll("[data-child-run]").forEach((group) => groups.set(Number(group.dataset.childRun), group.querySelector("ol")));
  } else {
    delete li.dataset.streamCheckpoint;
    mainSteps.replaceChildren();
    answerText.textContent = "";
    answer.hidden = true;
    document.querySelectorAll(`[data-live-user="${runId}"]`).forEach((bubble) => bubble.remove());
  }

  let spoken = label.textContent;
  const say = (text) => { spoken = text; label.textContent = text; };
  const stepsFor = (id) => groups.get(id) || mainSteps;
  const lastStep = (list) => list.lastElementChild;
  const appendStep = (list, kind) => {
    const step = fromTemplate(li.querySelector(`template[data-step="${kind}"]`));
    list.appendChild(step);
    return step;
  };
  const appendText = (list, kind, text) => {
    const last = lastStep(list);
    const step = last && last.dataset.step === kind ? last : appendStep(list, kind);
    step.querySelector("[data-step-text]").textContent += text;
  };
  const flushAnswer = () => {
    if (answer.hidden || !answerText.textContent) return;
    appendStep(mainSteps, "note").querySelector("[data-step-text]").textContent = answerText.textContent;
    answerText.textContent = "";
    answer.hidden = true;
  };

  const after = resume ? `&after=${encodeURIComponent(stored.lastId)}` : "";
  const source = new EventSource(`/chat/${thread}/events?run=${runId}${after}`);
  liveRuns.set(runId, { li, source });
  const stop = () => {
    source.close();
    if (liveRuns.get(runId)?.source === source) liveRuns.delete(runId);
  };
  const on = (kind, handler) => source.addEventListener(kind, (event) => {
    if (!li.isConnected) { stop(); return; }
    handler(JSON.parse(event.data));
    if (!event.lastEventId || settled) return;
    li.dataset.streamCheckpoint = event.lastEventId;
    sessionStorage.setItem(checkpointKey(thread), JSON.stringify({ run: runId, lastId: Number(event.lastEventId) }));
  });

  on("run_start", (data) => {
    if (data.run_id === runId) return;
    const group = appendStep(mainSteps, "subagent");
    group.dataset.childRun = data.run_id;
    group.querySelector("[data-child-role]").textContent = data.role || data.thread_kind || "";
    group.querySelector("[data-child-title]").textContent = data.title || "";
    groups.set(data.run_id, group.querySelector("ol"));
    say(`waiting for ${data.role || "subagent"}…`);
  });
  on("thinking", (data) => {
    if (data.run_id === runId) { flushAnswer(); say("thinking…"); }
    appendText(stepsFor(data.run_id), "thinking", data.text);
  });
  on("text", (data) => {
    if (data.run_id !== runId) {
      appendText(stepsFor(data.run_id), "note", data.text);
      return;
    }
    answer.hidden = false;
    answerText.textContent += data.text;
    say("writing…");
  });
  on("tool_start", (data) => {
    if (data.run_id === runId) { flushAnswer(); say(`running ${data.name}…`); }
    const step = appendStep(stepsFor(data.run_id), "tool");
    step.dataset.toolId = data.id;
    step.querySelector("[data-tool-name]").textContent = toolVerb(data.name);
    step.querySelector("[data-tool-args]").textContent = data.args || "";
  });
  on("tool_done", (data) => {
    const step = li.querySelector(`[data-tool-id="${CSS.escape(data.id)}"]`);
    if (!step) return;
    step.querySelector("[data-tool-result]").textContent = data.preview || "";
    const state = step.querySelector("[data-tool-state]");
    if (data.ok) state.remove();
    else { state.textContent = "error"; state.dataset.variant = "destructive"; }
    if (data.run_id === runId) say("thinking…");
  });
  on("user", (data) => {
    const queued = document.querySelector(`[data-queued="${data.queue_id}"]`);
    const bubble = userMessage(userTemplate, data.text);
    bubble.dataset.liveUser = runId;
    li.before(bubble);
    if (queued) queued.remove();
  });
  on("plan", refreshWork);
  on("files", refreshWork);
  on("run_done", (data) => {
    if (data.run_id === runId) return;
    const group = li.querySelector(`[data-child-run="${data.run_id}"] [data-child-state]`);
    if (group) { group.textContent = data.state; if (data.state !== "done") group.dataset.variant = "destructive"; }
    refreshWork();
  });
  let settled = false;
  const settle = (completed = false) => {
    if (settled) return;
    settled = true;
    stop();
    if (readCheckpoint(thread).run === runId) sessionStorage.removeItem(checkpointKey(thread));
    setBusy(false);
    document.querySelectorAll(`[data-live-user="${runId}"]`).forEach((bubble) => bubble.remove());
    const announcer = document.getElementById("transcript-announcer");
    if (completed && announcer) announcer.textContent = "Reply ready.";
    if (li.isConnected) htmx.trigger(li, "run-done");
  };
  on("complete", () => settle(true));
  on("run_error", () => settle(true));
  on("done", () => settle(true));
  source.onopen = () => {
    refusedRuns.delete(runId);
    label.textContent = spoken;
  };
  source.onerror = () => {
    if (settled) return;
    if (!li.isConnected) { stop(); return; }
    label.textContent = "reconnecting…";
    if (source.readyState !== EventSource.CLOSED) return;
    const refused = (refusedRuns.get(runId) || 0) + 1;
    refusedRuns.set(runId, refused);
    setTimeout(settle, refused === 1 ? 0 : SLOW_RETRY_MS);
  };
  if (!watchedRuns.has(li)) {
    watchedRuns.add(li);
    li.addEventListener("htmx:beforeSwap", (event) => {
      if (event.detail.elt === li && event.detail.xhr.status === 200 && !event.detail.serverResponse.includes("data-live-run=")) {
        document.querySelectorAll("#transcript [data-queued]").forEach((row) => row.remove());
      }
    });
    li.addEventListener("htmx:afterRequest", (event) => {
      if (event.detail.elt === li && !event.detail.successful) setTimeout(() => { if (li.isConnected) connectLive(li); }, SLOW_RETRY_MS);
    });
  }
  setBusy(true);
};

const connectAll = () => document.querySelectorAll("[data-live-run]").forEach(connectLive);

document.addEventListener("DOMContentLoaded", connectAll);
document.addEventListener("htmx:afterSettle", (event) => {
  connectAll();
  const form = document.getElementById("composer");
  if (form && event.detail.elt === form) {
    form.elements.edit.value = "";
    document.getElementById("composer-editing").hidden = true;
  }
  const transcript = document.getElementById("transcript");
  if (transcript && transcript.contains(event.detail.target)) transcript.lastElementChild?.scrollIntoView({ block: "end" });
  focusFallback(event.detail.target);
});

const uncertainSend = (xhr) => {
  if (xhr.status === 0 || xhr.status === 408 || xhr.status === 429 || xhr.status >= 500) return true;
  try {
    return JSON.parse(xhr.responseText).error?.code === "idempotency_in_progress";
  } catch {
    return false;
  }
};

const commandKey = () => Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, "0")).join("");

document.addEventListener("submit", (event) => {
  const form = event.target;
  if (form.id !== "composer") return;
  const key = form.dataset.commandKey || commandKey();
  form.dataset.commandKey = key;
  form.setAttribute("hx-headers", JSON.stringify({ "Idempotency-Key": key }));
  const transcript = document.getElementById("transcript");
  const text = form.elements.text.value.trim();
  if (!transcript || !text || form.elements.edit.value) return;
  transcript.querySelectorAll(`[data-command-key="${CSS.escape(key)}"]`).forEach((bubble) => bubble.remove());
  const bubble = userMessage(form.querySelector("template[data-message=user]"), text);
  bubble.dataset.commandKey = key;
  transcript.appendChild(bubble);
  bubble.scrollIntoView({ block: "end" });
}, true);

document.addEventListener("htmx:afterRequest", (event) => {
  const form = event.detail.elt;
  if (form.id !== "composer") return;
  const key = event.detail.requestConfig?.headers?.["Idempotency-Key"];
  if (!key) return;
  document.querySelectorAll(`#transcript [data-command-key="${CSS.escape(key)}"]`).forEach((bubble) => bubble.remove());
  const xhr = event.detail.xhr;
  if (!(event.detail.successful && xhr.getResponseHeader("Atrium-Command-Id")) && uncertainSend(xhr)) return;
  if (form.dataset.commandKey === key) delete form.dataset.commandKey;
  form.removeAttribute("hx-headers");
});

["input", "change"].forEach((kind) => document.addEventListener(kind, (event) => {
  const form = event.target.closest?.("#composer");
  if (form) delete form.dataset.commandKey;
}));

document.addEventListener("submit", (event) => {
  if (!event.target.matches("form[data-test-signin]")) return;
  const status = document.getElementById("oidc-test");
  htmx.ajax("GET", `/settings/auth/test/status?after=${status?.dataset.seq || 0}`, { target: "#oidc-test", swap: "outerHTML" });
});

document.addEventListener("input", (event) => {
  if (!event.target.closest("form[data-test-signin]")) return;
  const status = document.getElementById("oidc-test");
  if (!status || status.dataset.state === "idle") return;
  const idle = Object.assign(document.createElement("div"), { id: "oidc-test" });
  idle.dataset.state = "idle";
  idle.dataset.seq = status.dataset.seq;
  status.replaceWith(idle);
});

const pushState = { registration: null, subscription: null };
const pushSection = () => document.querySelector("[data-push]");
const pushBytes = (text) => {
  const raw = atob(text.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (text.length % 4)) % 4));
  return Uint8Array.from(raw, (c) => c.charCodeAt(0));
};
const pushText = (buffer) => btoa(String.fromCharCode(...new Uint8Array(buffer))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

const pushSubmit = (name, fields) => {
  const form = pushSection().querySelector(`[data-push-form="${name}"]`);
  Object.entries(fields).forEach(([key, value]) => { form.elements[key].value = value; });
  form.submit();
};

actions["push-enable"] = async (button) => {
  const section = pushSection();
  const key = pushBytes(section.dataset.pushKey);
  button.disabled = true;
  try {
    let subscription = pushState.subscription;
    if (subscription && pushText(subscription.options.applicationServerKey) !== pushText(key)) {
      await subscription.unsubscribe();
      subscription = null;
    }
    subscription ||= await pushState.registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
    pushSubmit("subscribe", { endpoint: subscription.endpoint, p256dh: pushText(subscription.getKey("p256dh")), auth: pushText(subscription.getKey("auth")) });
  } catch {
    button.disabled = false;
  }
};

actions["push-disable"] = async (button) => {
  button.disabled = true;
  const subscription = await pushState.registration.pushManager.getSubscription();
  if (!subscription) {
    location.reload();
    return;
  }
  const endpoint = subscription.endpoint;
  await subscription.unsubscribe();
  pushSubmit("unsubscribe", { endpoint });
};

document.addEventListener("DOMContentLoaded", async () => {
  const section = pushSection();
  if (!section?.dataset.pushKey || !("serviceWorker" in navigator) || !("PushManager" in window)) return;
  await navigator.serviceWorker.register("/static/sw.js", { scope: "/" });
  pushState.registration = await navigator.serviceWorker.ready;
  pushState.subscription = await pushState.registration.pushManager.getSubscription();
  const endpoint = pushState.subscription?.endpoint;
  const known = Boolean(endpoint) && [...section.querySelectorAll("[data-push-device]")].some((row) => row.dataset.pushDevice === endpoint);
  section.querySelector('[data-action="push-enable"]').hidden = known || globalThis.Notification?.permission === "denied";
  section.querySelector('[data-action="push-disable"]').hidden = !known;
});

actions["fill-composer"] = (button) => {
  const field = document.getElementById(button.dataset.target);
  if (!field) return;
  field.value = button.dataset.text;
  field.focus();
  field.setSelectionRange(field.value.length, field.value.length);
};

const palette = () => document.getElementById("palette");
const paletteInput = () => document.getElementById("palette-input");
let paletteRequest = null;
let paletteTimer = 0;

const fuzzyScore = (query, text) => {
  if (!query) return 1;
  const q = query.toLowerCase();
  const t = text.toLowerCase();
  const at = t.indexOf(q);
  if (at >= 0) return 1000 - at;
  let score = 0;
  let from = 0;
  let previous = -2;
  for (const ch of q) {
    if (ch === " ") continue;
    const i = t.indexOf(ch, from);
    if (i < 0) return 0;
    score += i === previous + 1 ? 5 : 1;
    previous = i;
    from = i + 1;
  }
  return score;
};

const paletteOptions = () => [...palette().querySelectorAll('[role="option"]')].filter((option) => !option.closest("[hidden]"));

const paletteSelect = (option) => {
  const input = paletteInput();
  palette().querySelectorAll('[role="option"][aria-selected="true"]').forEach((other) => other.setAttribute("aria-selected", "false"));
  if (!option) {
    input.removeAttribute("aria-activedescendant");
    return;
  }
  option.setAttribute("aria-selected", "true");
  input.setAttribute("aria-activedescendant", option.id);
  option.scrollIntoView({ block: "nearest" });
};

const paletteFilter = () => {
  const dialog = palette();
  const query = paletteInput().value.trim();
  dialog.querySelectorAll('[role="group"]').forEach((group) => {
    const threads = group.dataset.paletteGroup === "threads";
    const ranked = [...group.querySelectorAll('[role="option"]')].map((option, index) => {
      option.dataset.order ??= String(index);
      const text = [...option.querySelectorAll("span")].map((span) => span.textContent).join(" ");
      return { option, score: threads ? 1 : fuzzyScore(query, text) };
    });
    ranked.forEach(({ option, score }) => { option.hidden = score === 0; });
    if (!threads) {
      ranked.filter(({ score }) => score > 0)
        .sort((a, b) => b.score - a.score || Number(a.option.dataset.order) - Number(b.option.dataset.order))
        .forEach(({ option }) => group.appendChild(option));
    }
    group.hidden = ranked.every(({ score }) => score === 0);
  });
  const options = paletteOptions();
  dialog.querySelector("[data-palette-empty]").hidden = options.length > 0;
  paletteSelect(options[0]);
};

const paletteThreads = () => {
  const url = palette().dataset.paletteThreads;
  if (!url) return;
  clearTimeout(paletteTimer);
  paletteTimer = setTimeout(async () => {
    paletteRequest?.abort();
    paletteRequest = new AbortController();
    try {
      const response = await fetch(`${url}?q=${encodeURIComponent(paletteInput().value.trim())}`, { signal: paletteRequest.signal, credentials: "same-origin" });
      if (!response.ok) return;
      const template = document.createElement("template");
      template.innerHTML = await response.text();
      palette().querySelector("[data-palette-thread-options]").replaceChildren(template.content);
      paletteFilter();
    } catch {
      return;
    }
  }, 120);
};

const openPalette = () => {
  const dialog = palette();
  if (!dialog || dialog.open) return;
  const opener = document.activeElement;
  paletteInput().value = "";
  dialog.showModal();
  if (opener && opener.id) dialogOpeners.set(dialog, opener.id);
  paletteFilter();
  paletteThreads();
  paletteInput().focus();
};

const paletteRun = (option) => {
  const dialog = palette();
  const href = option.dataset.paletteHref;
  const theme = option.dataset.paletteTheme;
  if (href) {
    dialog.close();
    location.assign(href);
    return;
  }
  if (theme) {
    const button = document.querySelector(`.sb-theme [data-theme-value="${theme}"]`);
    if (button) actions["set-theme"](button);
  }
  if (option.hasAttribute("data-palette-sidebar")) {
    if (window.matchMedia("(min-width: 768px)").matches) {
      const button = document.querySelector('[data-action="toggle-rail"]');
      if (button) actions["toggle-rail"](button);
    } else {
      dialogOpeners.delete(dialog);
      dialog.close();
      sidebarOpen(!document.getElementById("sidebar")?.hasAttribute("data-open"));
      return;
    }
  }
  dialog.close();
};

actions["open-palette"] = openPalette;

document.addEventListener("keydown", (event) => {
  if ((event.metaKey || event.ctrlKey) && !event.altKey && !event.shiftKey && event.key.toLowerCase() === "k") {
    const dialog = palette();
    if (!dialog) return;
    event.preventDefault();
    if (dialog.open) dialog.close();
    else openPalette();
    return;
  }
  if (event.target.id !== "palette-input") return;
  const options = paletteOptions();
  const current = options.findIndex((option) => option.getAttribute("aria-selected") === "true");
  const step = { ArrowDown: 1, ArrowUp: -1 }[event.key];
  if (step) {
    event.preventDefault();
    if (options.length) paletteSelect(options[(current + step + options.length) % options.length]);
  } else if (event.key === "Home" || event.key === "End") {
    event.preventDefault();
    paletteSelect(event.key === "Home" ? options[0] : options[options.length - 1]);
  } else if (event.key === "Enter") {
    event.preventDefault();
    if (current >= 0) paletteRun(options[current]);
  }
});

document.addEventListener("input", (event) => {
  if (event.target.id !== "palette-input") return;
  paletteFilter();
  paletteThreads();
});

document.addEventListener("click", (event) => {
  const option = event.target.closest && event.target.closest('#palette [role="option"]');
  if (option) {
    paletteRun(option);
    return;
  }
  if (event.target.id === "palette") palette().close();
});

const refPicker = () => document.getElementById("ref-picker");
const refField = () => document.querySelector("textarea[data-submit-on-enter]");
let refRequest = null;
let refTimer = 0;
let refSpan = null;

const refTokenAt = (field) => {
  const caret = field.selectionStart;
  if (caret !== field.selectionEnd) return null;
  const before = field.value.slice(0, caret);
  const match = /(?:^|\s)@([^\s@]*)$/.exec(before);
  if (!match) return null;
  return { start: caret - match[1].length - 1, end: caret, query: match[1] };
};

const refOptions = () => [...refPicker().querySelectorAll('[role="option"]')];

const refSelect = (option) => {
  const list = refPicker();
  list.querySelectorAll('[role="option"][aria-selected="true"]').forEach((other) => other.setAttribute("aria-selected", "false"));
  if (!option) {
    list.removeAttribute("aria-activedescendant");
    return;
  }
  option.setAttribute("aria-selected", "true");
  list.setAttribute("aria-activedescendant", option.id);
  option.scrollIntoView({ block: "nearest" });
};

const refClose = () => {
  const list = refPicker();
  if (list) list.hidden = true;
  refSpan = null;
  clearTimeout(refTimer);
  refRequest?.abort();
  refRequest = null;
};

const refFill = () => {
  const field = refField();
  const list = refPicker();
  if (!field || !list) return;
  const span = refTokenAt(field);
  if (!span) {
    refClose();
    return;
  }
  refSpan = span;
  clearTimeout(refTimer);
  refTimer = setTimeout(async () => {
    refRequest?.abort();
    refRequest = new AbortController();
    try {
      const thread = document.getElementById("workspace")?.dataset.thread || "";
      const query = new URLSearchParams({ q: span.query, ref: thread });
      const response = await fetch(`/chat/palette?${query}`, { signal: refRequest.signal, credentials: "same-origin" });
      if (!response.ok) return;
      const template = document.createElement("template");
      template.innerHTML = await response.text();
      list.replaceChildren(template.content);
      const options = refOptions();
      refSelect(options[0]);
      list.hidden = options.length === 0;
    } catch {
      return;
    }
  }, 120);
};

const refPick = (option) => {
  const field = refField();
  const span = refSpan;
  if (!field || !span || !option) return;
  const token = option.dataset.refToken;
  if (!token) return;
  const after = field.value.slice(span.end);
  field.value = field.value.slice(0, span.start) + token + " " + after;
  const caret = span.start + token.length + 1;
  field.focus();
  field.setSelectionRange(caret, caret);
  refClose();
};

document.addEventListener("input", (event) => {
  if (event.target !== refField()) return;
  refFill();
});

document.addEventListener("keydown", (event) => {
  if (event.target !== refField()) return;
  const list = refPicker();
  if (!list || list.hidden) return;
  const options = refOptions();
  const current = options.findIndex((option) => option.getAttribute("aria-selected") === "true");
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    event.preventDefault();
    const step = event.key === "ArrowDown" ? 1 : -1;
    if (options.length) refSelect(options[(current + step + options.length) % options.length]);
  } else if (event.key === "Enter" && !event.metaKey && !event.ctrlKey && !event.altKey) {
    event.preventDefault();
    refPick(options[current]);
  } else if (event.key === "Escape") {
    event.preventDefault();
    refClose();
  }
});

document.addEventListener("mousedown", (event) => {
  const option = event.target.closest && event.target.closest('#ref-picker [role="option"]');
  if (option) event.preventDefault();
});

document.addEventListener("click", (event) => {
  const option = event.target.closest && event.target.closest('#ref-picker [role="option"]');
  if (option) refPick(option);
});

document.addEventListener("focusout", (event) => {
  if (event.target !== refField()) return;
  setTimeout(() => {
    if (document.activeElement !== refField()) refClose();
  }, 100);
});

document.addEventListener("submit", () => refClose());

function syncAskOpts() {
  document.querySelectorAll("[data-ask-opts]").forEach((box) => {
    const model = box.querySelector('select[name="model"]');
    const effort = box.querySelector('select[name="effort"]');
    if (!model || !effort) return;
    box.querySelector("[data-opts-label]").textContent = `${model.selectedOptions[0].textContent} · ${effort.value}`;
  });
}

document.addEventListener("change", (event) => {
  if (event.target.closest("[data-ask-opts]")) syncAskOpts();
});

document.addEventListener("htmx:afterSettle", syncAskOpts);

document.addEventListener("click", (event) => {
  document.querySelectorAll("details[data-menu][open]").forEach((menu) => {
    if (!menu.contains(event.target)) menu.open = false;
  });
});

document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
  document.querySelectorAll("details[data-menu][open]").forEach((menu) => {
    menu.open = false;
  });
});

document.addEventListener("change", (event) => {
  const all = event.target.closest("[data-select-all]");
  if (!all) return;
  all.form.querySelectorAll('input[type="checkbox"][name="id"]').forEach((box) => {
    box.checked = all.checked;
  });
});

document.addEventListener("change", (event) => {
  const group = event.target.closest("[data-quality]");
  const pattern = document.getElementById("pattern");
  if (!group || !pattern || !group.dataset.base) return;
  const quality = event.target.value;
  pattern.value = quality === "any" ? group.dataset.base : `${group.dataset.base} ${quality}`;
  pattern.dispatchEvent(new Event("input", { bubbles: true }));
});

document.addEventListener("DOMContentLoaded", () => {
  if (!window.matchMedia("(width < 48rem)").matches) document.querySelector("[data-focus-wide]")?.focus();
});
