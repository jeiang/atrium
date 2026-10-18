// PROTOTYPE: minimal client behaviour so the pickers feel real. Not production code.
const $ = (s, r = document) => r.querySelector(s);
const M = (id) => window.MODELS.find((m) => m.ID === id);
const EFFORTS = ["low", "medium", "high", "max"];
const k = (t) => (t >= 1e6 ? t / 1e6 + "M" : t / 1e3 + "K");
const money = (f) => "$" + f.toFixed(f < 0.1 ? 3 : 2);
const mult = (m, t) => (m.TierCliff && t > m.TierCliff ? m.TierMult : 1);
const read = (m, t) => (t / 1e6) * m.Read * mult(m, t);
const write = (m, t) => (t / 1e6) * m.W1h * mult(m, t);
const thinkOk = (t, e) => !t.MaxEffort || EFFORTS.indexOf(e) <= EFFORTS.indexOf(t.MaxEffort);

function refreshNew(changed) {
  const f = $("#new-chat");
  if (!f) return;
  const m = M($("#model").value);
  if (changed === "model") {
    $("#effort").value = m.DefaultEffort;
    $("#ctx").value = m.DefaultCtx;
  }
  const effort = $("#effort").value;
  for (const o of $("#effort").options) o.textContent = o.value + (o.value === m.DefaultEffort ? " (default for " + m.Name + ")" : "");
  const ctx = $("#ctx");
  const cur = +ctx.value;
  ctx.innerHTML = window.CTX.map((c) => `<option value="${c}" ${c === cur ? "selected" : ""}>${k(c)} tokens · ≈${money(read(m, c))} per step when full</option>`).join("");
  const think = $("#think");
  const prev = think.value;
  think.innerHTML = m.Thinking.map((t) => `<option value="${t.ID}" ${thinkOk(t, effort) ? "" : "disabled"}>${t.Label}${t.Note ? " (" + t.Note + ")" : ""}</option>`).join("");
  think.value = [...think.options].find((o) => o.value === prev && !o.disabled) ? prev : [...think.options].find((o) => !o.disabled).value;
  $("#ctx-cost").textContent = `A full cold start writes ≈${money(write(m, cur))} to the cache; later steps read it at ≈${money(read(m, cur))}. Editable later; applies at the next cold boundary.`;
  const cliff = $("#cliff");
  cliff.hidden = !(m.TierCliff && cur > m.TierCliff);
  if (!cliff.hidden) cliff.querySelector("p").textContent = `Past 100K tokens Haiku bills the whole request at the long-context tier (≈2× per token). 90K stays under it; at ${k(cur)} each step costs ≈${money(read(m, cur))} instead of ≈${money(read(m, 90000))} at 90K.`;
  $("#start-line").textContent = `Starting a chat costs nothing until you send. First message writes ≈${money(write(m, 12000))} of prompt prefix to the cache (1 h TTL); every later message in the hour reads it at ≈${money(read(m, 12000))}.`;
}

function setMode(coding) {
  $("#coding").hidden = !coding;
  document.querySelectorAll("[data-mode]").forEach((b) => {
    const on = (b.dataset.mode === "coding") === coding;
    b.dataset.variant = on ? "secondary" : "outline";
    b.setAttribute("aria-pressed", on);
  });
}

function refreshSwitch() {
  const side = $("#side");
  const sel = $("#t-model");
  if (!side || !sel) return;
  const cur = M(side.dataset.model), to = M(sel.value), cached = +side.dataset.cached, warm = +side.dataset.warm;
  const box = $("#switch-line");
  if (to.ID === cur.ID) { box.hidden = true; return; }
  box.hidden = false;
  const down = window.MODELS.indexOf(to) < window.MODELS.indexOf(cur);
  const rw = write(to, cached);
  box.innerHTML = warm
    ? `<div class="alert border-warning/40 bg-warning/10 text-warning"><h3>Switching now costs ≈${money(rw)}</h3><p>${to.Name} can't reuse ${cur.Name}'s cache, so the ${k(cached)}-token prefix is rewritten once (≈${money(rw)}), then ≈${money(read(to, cached))} per step (now ≈${money(read(cur, cached))}). The cache is warm for ${warm} more min; if you wait until it goes cold the switch is free.${down ? ` Switching down also drops ${cur.Name}'s thinking blocks.` : ""}</p></div><div class="flex gap-2"><button type="button" class="btn" data-size="sm">Switch to ${to.Name}</button></div>`
    : `<div class="alert"><h3>Switching is free right now</h3><p>The cache is cold, so the next message rewrites it whichever model runs it (≈${money(rw)} at ${to.Name} rates).${down ? ` Switching down drops ${cur.Name}'s thinking blocks from the history: ${to.Name} can't read them.` : ""}</p></div><div class="flex gap-2"><button type="button" class="btn" data-size="sm">Switch to ${to.Name}</button></div>`;
}

document.addEventListener("change", (e) => {
  if (e.target.id === "model") refreshNew("model");
  else if (e.target.id === "effort" || e.target.id === "ctx") refreshNew();
  else if (e.target.id === "t-model") refreshSwitch();
  else if (e.target.id === "effort-msg") {
    const base = e.target.dataset.base;
    $("#effort-note").hidden = e.target.value === base;
  }
});
document.addEventListener("click", (e) => {
  const b = e.target.closest("[data-mode]");
  if (b) setMode(b.dataset.mode === "coding");
});
