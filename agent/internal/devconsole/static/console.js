"use strict";
const $ = (id) => document.getElementById(id);
const post = (path, body, method = "POST") =>
  fetch(path, { method, headers: { "Content-Type": "application/json", "X-Device-Console": "1" }, body: body ? JSON.stringify(body) : undefined })
    .then(async (r) => { const j = await r.json().catch(() => ({})); if (!r.ok) throw new Error(j.error || r.statusText); return j; });

let answered = new Set();

function el(tag, attrs = {}, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) k === "class" ? (e.className = v) : (e[k] = v);
  for (const k of kids) e.append(k);
  return e;
}

function renderPrompts(prompts) {
  const box = $("prompts");
  box.replaceChildren();
  for (const p of prompts) {
    if (answered.has(p.id)) continue;
    const secs = p.expires ? Math.max(0, Math.round((new Date(p.expires) - Date.now()) / 1000)) : null;
    const actions = el("div", { class: "actions" },
      el("button", { class: "primary", textContent: p.new_key ? "Allow and remember" : "Allow", onclick: () => answer(p.id, p.new_key ? "remember" : "once") }));
    if (p.new_key) actions.append(el("button", { class: "secondary", textContent: "Allow once", onclick: () => answer(p.id, "once") }));
    actions.append(el("button", { class: "ghost", textContent: "Deny", onclick: () => answer(p.id, "deny") }));
    box.append(el("div", { class: "card prompt" },
      el("h2", { textContent: p.title }),
      el("div", { textContent: p.message }),
      el("div", { class: "muted", textContent: secs !== null ? `Expires in ${secs}s` : "" }),
      actions));
  }
}

async function answer(id, a) {
  answered.add(id);
  try { await post(`/api/prompts/${id}`, { answer: a }); } catch (e) { alertBox(e.message); }
  refresh();
}

function alertBox(msg) { $("enroll-error").textContent = msg; }

function render(s) {
  const st = s.status;
  const live = st.session && st.session.State === "active";
  $("dot").className = "dot " + (live ? "live" : st.online ? "online" : "");
  $("hostline").textContent = `${st.hostname || ""} · ${st.version || ""}`;
  $("enroll-card").classList.toggle("hidden", st.enrolled);
  $("status-card").classList.toggle("hidden", !st.enrolled);
  $("pins-card").classList.toggle("hidden", !st.enrolled);
  if (!st.enrolled && !$("server-url").value) $("server-url").value = st.server_url || "";
  $("s-name").textContent = `${st.name || "—"} (${st.device_id || ""})`;
  $("s-server").textContent = st.server_url;
  $("s-conn").textContent = st.fatal ? `Refused by server: ${st.fatal}` : st.online ? "Online" : `Offline${st.last_error ? " — " + st.last_error : ""}`;
  $("s-encoder").textContent = st.encoder;
  $("s-version").textContent = st.version;
  if (document.activeElement !== $("pause")) $("pause").checked = st.paused;

  const banner = $("session-banner");
  banner.classList.toggle("hidden", !st.session);
  if (st.session) {
    const i = st.session;
    $("session-detail").textContent = `${i.Label || "browser"} · ${i.Email || ""} · ${i.State}${i.Path ? " via " + i.Path : ""}`;
  }
  renderPrompts(s.prompts || []);
  const ul = $("pins");
  ul.replaceChildren();
  for (const p of s.pins || []) {
    ul.append(el("li", {},
      el("div", {}, el("div", { textContent: p.label || "browser" }), el("div", { class: "muted", textContent: `approved ${new Date(p.approved_at).toLocaleString()}` })),
      el("button", { class: "ghost", textContent: "Remove", onclick: () => post(`/api/pins/${p.public_key_sha256}`, null, "DELETE").then(refresh) })));
  }
  if (!(s.pins || []).length) ul.append(el("li", { class: "muted", textContent: "None yet." }));
}

async function refresh() {
  try { render(await (await fetch("/api/state", { cache: "no-store" })).json()); } catch (_) { /* agent restarting */ }
}

$("enroll-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  alertBox("");
  try { await post("/api/enroll", { server_url: $("server-url").value, token: $("token").value }); $("token").value = ""; }
  catch (err) { alertBox(err.message); }
  refresh();
});
$("token").addEventListener("input", (e) => {
  let v = e.target.value.toUpperCase().replace(/[^A-Z0-9]/g, "").slice(0, 8);
  if (v.length > 4) v = v.slice(0, 4) + "-" + v.slice(4);
  e.target.value = v;
});
$("end-btn").addEventListener("click", () => post("/api/session/end").then(refresh));
$("pause").addEventListener("change", (e) => post("/api/pause", { paused: e.target.checked }).then(refresh));

refresh();
setInterval(refresh, 1000);
