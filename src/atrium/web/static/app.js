(() => {
  const dark = window.matchMedia("(prefers-color-scheme: dark)");
  const apply = () => document.documentElement.classList.toggle("dark", dark.matches);
  apply();
  dark.addEventListener("change", apply);
})();

document.addEventListener("htmx:configRequest", (event) => {
  if (event.detail.verb !== "get") return;
  const data = event.detail.formData;
  [...data.keys()].forEach((key) => {
    if (data.getAll(key).every((value) => value === "")) data.delete(key);
  });
});

document.addEventListener("htmx:historyRestore", () => {
  const params = new URLSearchParams(window.location.search);
  document.querySelectorAll("form:is([hx-push-url], [hx-replace-url]) [name]").forEach((field) => {
    if (field.type === "checkbox") field.checked = params.has(field.name);
    else if (field.type === "radio") field.checked = field.value === (params.get(field.name) ?? "");
    else if (field.type !== "hidden") field.value = params.get(field.name) ?? "";
  });
  window.basecoat.initAll({ force: true });
});

document.addEventListener("htmx:beforeRequest", (event) => {
  const target = event.detail.target;
  const loading = target && target.dataset.loading && document.getElementById(target.dataset.loading);
  if (loading) target.replaceChildren(loading.content.cloneNode(true));
  const dialog = target && target.closest("dialog");
  if (dialog && !dialog.open) dialog.showModal();
});

document.addEventListener("close-dialogs", () => {
  document.querySelectorAll("dialog[open]").forEach((dialog) => dialog.close());
});

document.addEventListener("htmx:beforeSwap", (event) => {
  const xhr = event.detail.xhr;
  if (xhr.status === 422 && (xhr.getResponseHeader("content-type") || "").startsWith("text/html")) {
    event.detail.shouldSwap = true;
    event.detail.isError = false;
  }
});

document.addEventListener("htmx:afterRequest", (event) => {
  const form = event.detail.elt;
  if (form.matches && form.matches("form[data-reset]") && event.detail.successful) form.reset();
  if (form.matches && form.matches("button[data-suggestion]") && event.detail.successful) {
    const item = form.closest("li");
    const next = item.nextElementSibling || item.previousElementSibling;
    item.remove();
    (next ? next.querySelector("button") : document.getElementById("add-condition")).focus();
  }
});

document.addEventListener("htmx:afterSwap", (event) => {
  const target = event.detail.target || event.target;
  if (target && target.id === "conditions") htmx.trigger("#rule-form", "change");
});

document.addEventListener("submit", (event) => {
  const question = event.target.dataset && event.target.dataset.confirmSubmit;
  if (question && !window.confirm(question)) event.preventDefault();
});

const failed = (title, description) => (event) => {
  const target = event.detail.target;
  if (target && target.dataset.loading) {
    target.replaceChildren();
    const dialog = target.closest("dialog");
    if (dialog) dialog.close();
  }
  document.getElementById("toaster").toast({ category: "error", title, description });
};
document.addEventListener("htmx:responseError", failed("Something went wrong", "atrium could not finish that request. Try again, or reload the page."));
document.addEventListener("htmx:sendError", failed("atrium is unreachable", "Check the connection to the server and try again."));

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
  const nav = document.getElementById("sidebar");
  if (event.key === "Escape" && nav && nav.close && window.innerWidth < 768 && nav.getAttribute("aria-hidden") === "false") {
    nav.close();
    document.getElementById("nav-toggle").focus();
  }
});

const actions = {
  "toggle-sidebar": () => document.getElementById("sidebar").toggle(),
  "close-dialog": (button) => button.closest("dialog").close(),
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

// Add-torrent form: the zakkart shim opens /media/add#torrent=<base64url>&name=<file> so the bytes never reach a
// URL log; a locally chosen .torrent is read the same way. Both land in the hidden "torrent" and "name" fields.
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

// Composer attachments: files picked or dropped on a form[data-attach] accumulate in a DataTransfer that backs the
// hidden file input, shown as chips until the form is sent or reset.
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
    chip.innerHTML = `<span class="truncate"></span><span class="text-muted-foreground tabular-nums"></span><button type="button" class="rounded-sm hover:text-destructive" data-action="remove-file" data-index="${index}" aria-label="Remove"><span aria-hidden="true">×</span></button>`;
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
  if (event.target.matches("form[data-attach]")) { pendingFiles.delete(event.target); event.target.querySelector("[data-chips]").replaceChildren(); }
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

// Thread workspace: the right panel (Work/Preview tabs, drag to resize, drawer on small screens), the composer's
// edit mode, and the live run view fed by server-sent events written by whichever process executes the run.
const PANEL_WIDTH_KEY = "atrium.panel.width";
const PANEL_MIN = 260;

const workspace = () => document.getElementById("workspace");
const panelOpen = (open) => {
  const panel = document.getElementById("panel");
  if (!panel) return;
  panel.toggleAttribute("data-open", open);
  document.getElementById("panel-backdrop").classList.toggle("hidden", !open);
  document.querySelectorAll('[data-action="toggle-panel"][aria-controls]').forEach((button) => button.setAttribute("aria-expanded", String(open)));
};

actions["toggle-panel"] = () => panelOpen(!document.getElementById("panel").hasAttribute("data-open"));
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

const showPreview = () => {
  const tab = document.getElementById("panel-tab-preview");
  if (!tab) return;
  if (tab.getAttribute("aria-selected") !== "true") tab.click();
  if (window.innerWidth < 768) panelOpen(true);
};

document.addEventListener("htmx:beforeRequest", (event) => {
  const target = event.detail.target;
  if (target && target.id === "preview-body") showPreview();
});

document.addEventListener("DOMContentLoaded", () => {
  const root = workspace();
  if (!root) return;
  const stored = Number(localStorage.getItem(PANEL_WIDTH_KEY));
  if (stored >= PANEL_MIN) root.style.setProperty("--panel-w", `${stored}px`);
  const handle = document.getElementById("panel-handle");
  const apply = (width) => {
    const clamped = Math.min(Math.max(width, PANEL_MIN), Math.floor(root.getBoundingClientRect().width * 0.6));
    root.style.setProperty("--panel-w", `${clamped}px`);
    return clamped;
  };
  handle.addEventListener("pointerdown", (event) => {
    event.preventDefault();
    handle.setPointerCapture(event.pointerId);
    const right = root.getBoundingClientRect().right;
    let width = parseFloat(getComputedStyle(root).getPropertyValue("--panel-w"));
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
    const width = apply(parseFloat(getComputedStyle(root).getPropertyValue("--panel-w")) + delta);
    localStorage.setItem(PANEL_WIDTH_KEY, String(Math.round(width)));
  });
});

document.addEventListener("keydown", (event) => {
  const panel = document.getElementById("panel");
  if (event.key === "Escape" && panel && panel.hasAttribute("data-open")) panelOpen(false);
});

const setBusy = (busy) => {
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

const toolMarkup = (data) => `
  <details class="rounded-md border bg-card">
    <summary class="flex cursor-pointer items-center gap-2 px-3 py-1.5 text-muted-foreground"><code class="font-mono text-foreground">${escapeHtml(data.name)}</code><span class="truncate font-mono">${escapeHtml(data.args || "")}</span><span class="badge ms-auto" data-variant="outline" data-tool-state>running</span></summary>
    <div class="border-t px-3 py-2 font-mono whitespace-pre-wrap break-words" data-tool-result>(no result yet)</div>
  </details>`;

const liveRuns = new Map();
const RETRY_MS = 2000;

const connectLive = (li) => {
  const runId = Number(li.dataset.liveRun);
  if (liveRuns.has(runId)) return;
  const thread = li.dataset.thread;
  const label = li.querySelector("[data-live-label]");
  const answer = li.querySelector("[data-live-answer]");
  const answerText = li.querySelector("[data-live-text]");
  const mainSteps = li.querySelector("[data-live-steps]");
  const groups = new Map([[runId, mainSteps]]);

  const stepsFor = (id) => groups.get(id) || mainSteps;
  const lastStep = (list) => list.lastElementChild;
  const appendStep = (list, kind, html) => {
    const step = document.createElement("li");
    step.dataset.step = kind;
    step.innerHTML = html;
    list.appendChild(step);
    return step;
  };
  const flushAnswer = () => {
    if (answer.hidden || !answerText.textContent) return;
    appendStep(mainSteps, "note", `<div class="whitespace-pre-wrap break-words">${escapeHtml(answerText.textContent)}</div>`);
    answerText.textContent = "";
    answer.hidden = true;
  };

  const source = new EventSource(`/chat/${thread}/events?run=${runId}`);
  liveRuns.set(runId, source);
  const on = (kind, handler) => source.addEventListener(kind, (event) => handler(JSON.parse(event.data)));

  on("run_start", (data) => {
    if (data.run_id === runId) return;
    const group = appendStep(mainSteps, "subagent", `<div class="flex flex-col gap-1 rounded-md border px-3 py-2"><span class="flex items-center gap-2 text-muted-foreground"><span class="badge" data-variant="secondary">${escapeHtml(data.role || data.thread_kind)}</span><span class="truncate">${escapeHtml(data.title || "")}</span><span class="badge ms-auto" data-variant="outline" data-child-state>running</span></span><ol class="flex flex-col gap-1.5 border-s ps-3"></ol></div>`);
    group.dataset.childRun = data.run_id;
    groups.set(data.run_id, group.querySelector("ol"));
    label.textContent = `waiting for ${data.role || "subagent"}…`;
  });
  on("thinking", (data) => {
    if (data.run_id === runId) { flushAnswer(); label.textContent = "thinking…"; }
    const list = stepsFor(data.run_id);
    const last = lastStep(list);
    if (last && last.dataset.step === "thinking") last.firstElementChild.textContent += data.text;
    else appendStep(list, "thinking", `<div class="whitespace-pre-wrap break-words text-muted-foreground">${escapeHtml(data.text)}</div>`);
  });
  on("text", (data) => {
    if (data.run_id !== runId) {
      const list = stepsFor(data.run_id);
      const last = lastStep(list);
      if (last && last.dataset.step === "note") last.firstElementChild.textContent += data.text;
      else appendStep(list, "note", `<div class="whitespace-pre-wrap break-words">${escapeHtml(data.text)}</div>`);
      return;
    }
    answer.hidden = false;
    answerText.textContent += data.text;
    label.textContent = "writing…";
  });
  on("tool_start", (data) => {
    if (data.run_id === runId) { flushAnswer(); label.textContent = `running ${data.name}…`; }
    const step = appendStep(stepsFor(data.run_id), "tool", toolMarkup(data));
    step.dataset.toolId = data.id;
  });
  on("tool_done", (data) => {
    const step = li.querySelector(`[data-tool-id="${CSS.escape(data.id)}"]`);
    if (!step) return;
    step.querySelector("[data-tool-result]").textContent = data.preview || "";
    const state = step.querySelector("[data-tool-state]");
    if (data.ok) state.remove();
    else { state.textContent = "error"; state.dataset.variant = "destructive"; }
    if (data.run_id === runId) label.textContent = "thinking…";
  });
  on("user", (data) => {
    const queued = document.querySelector(`[data-queued="${data.queue_id}"]`);
    const bubble = document.createElement("li");
    bubble.className = "flex gap-3";
    bubble.dataset.liveUser = runId;
    bubble.innerHTML = `<span class="mt-1 size-7 shrink-0 rounded-full bg-secondary" aria-hidden="true"></span><div class="min-w-0 flex-1 rounded-lg bg-secondary/60 px-4 py-3 text-sm whitespace-pre-wrap break-words"></div>`;
    bubble.lastElementChild.textContent = data.text;
    li.before(bubble);
    if (queued) queued.remove();
  });
  on("plan", refreshWork);
  on("files", refreshWork);
  on("run_done", (data) => {
    const group = li.querySelector(`[data-child-run="${data.run_id}"] [data-child-state]`);
    if (group) { group.textContent = data.state; if (data.state !== "done") group.dataset.variant = "destructive"; }
    refreshWork();
  });
  // Whatever ends the stream (the server's `done`, a dropped connection, a proxy timeout), the live view is
  // replaced by what the journal says: the run partial is the finished transcript, or a fresh live view that
  // reconnects and replays the run's events when the run is still going.
  let settled = false;
  const settle = () => {
    if (settled) return;
    settled = true;
    source.close();
    liveRuns.delete(runId);
    setBusy(false);
    refreshWork();
    document.querySelectorAll(`[data-live-user="${runId}"]`).forEach((bubble) => bubble.remove());
    if (li.isConnected) htmx.trigger(li, "run-done");
  };
  on("done", settle);
  source.onerror = () => setTimeout(settle, RETRY_MS);
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
});
