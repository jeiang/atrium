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
  "clear": (button) => document.getElementById(button.dataset.target).replaceChildren(),
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
