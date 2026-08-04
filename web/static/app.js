// kbind gateway UI — a pure gateway client. All state lives in the gateway's
// API; this page only renders it.
"use strict";

const $ = (id) => document.getElementById(id);
let provider = null;
let countdownTimer = null;

async function getJSON(url, opts) {
  const res = await fetch(url, opts);
  if (res.status === 401) throw { unauthenticated: true };
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error || res.statusText);
  return body;
}

function show(view) {
  for (const v of ["loading", "catalog", "error"]) {
    $("view-" + v).hidden = v !== view;
  }
}

// The login page is separate (/login); the app just sends you there.
function toLogin() {
  window.location.href = "/login";
}

async function init() {
  try {
    provider = await getJSON("/api/provider");
    $("provider-name").textContent = provider.name;
    $("provider-chip").hidden = false;
    document.title = provider.name + " · kbind";
  } catch (e) {
    $("error-text").textContent = "The gateway is not reachable: " + (e.message || e);
    show("error");
    return;
  }
  await loadCatalog();
}

async function loadCatalog() {
  try {
    const catalog = await getJSON("/api/catalog");
    renderCatalog(catalog);
    show("catalog");
    $("nav").hidden = false;
    loadWhoami();
  } catch (e) {
    if (e.unauthenticated) {
      toLogin();
      return;
    }
    $("error-text").textContent = e.message || String(e);
    show("error");
  }
}

async function loadWhoami() {
  try {
    const me = await getJSON("/api/me");
    $("whoami").textContent = me.displayName || me.subject;
    $("whoami").title = me.subject;
  } catch {
    $("whoami").textContent = "";
  }
}

function renderCatalog(catalog) {
  const root = $("catalog");
  root.textContent = "";
  const exports = catalog.exports || [];
  $("catalog-count").textContent =
    exports.length === 1 ? "1 service" : exports.length + " services";

  if (exports.length === 0) {
    const p = document.createElement("p");
    p.className = "muted";
    p.textContent = "Nothing is published yet. The provider curates offerings as Export objects (catalog.kbind.io).";
    root.appendChild(p);
    return;
  }

  const byName = new Map(exports.map((e) => [e.name, e]));
  const grouped = new Set();
  for (const col of catalog.collections || []) {
    const section = document.createElement("div");
    const title = document.createElement("div");
    title.className = "collection-title";
    title.textContent = col.title || col.name;
    section.appendChild(title);
    for (const name of col.exports) {
      const exp = byName.get(name);
      if (exp) {
        section.appendChild(exportRow(exp));
        grouped.add(name);
      }
    }
    root.appendChild(section);
  }

  const rest = exports.filter((e) => !grouped.has(e.name));
  if (rest.length > 0) {
    if (grouped.size > 0) {
      const title = document.createElement("div");
      title.className = "collection-title";
      title.textContent = "More services";
      root.appendChild(title);
    }
    for (const exp of rest) root.appendChild(exportRow(exp));
  }
}

function exportRow(exp) {
  const row = document.createElement("div");
  row.className = "export";

  const body = document.createElement("div");
  body.className = "export-body";

  const title = document.createElement("div");
  title.className = "export-title";
  title.textContent = exp.title || exp.name;
  body.appendChild(title);

  if (exp.description) {
    const desc = document.createElement("div");
    desc.className = "export-desc";
    desc.textContent = exp.description;
    body.appendChild(desc);
  }

  const apis = document.createElement("div");
  apis.className = "export-apis";
  for (const api of exp.apis || []) {
    const chip = document.createElement("span");
    chip.className = "api-name";
    chip.textContent = api;
    apis.appendChild(chip);
  }
  // Related resources (a core concept): auxiliary secrets/configmaps that
  // sync alongside the APIs. Shown up front so you know what flows.
  for (const rr of exp.relatedResources || []) {
    const chip = document.createElement("span");
    chip.className = "api-name related-chip";
    chip.textContent = (rr.direction === "FromConsumer" ? "⇧ " : "⇩ ") + rr.resource;
    chip.title = (rr.direction === "FromConsumer"
      ? "synced from your cluster to the provider"
      : "synced from the provider into your cluster")
      + (rr.selector ? " — " + rr.selector : "");
    apis.appendChild(chip);
  }
  body.appendChild(apis);

  if (exp.docs) {
    const docs = document.createElement("a");
    docs.className = "export-docs";
    docs.href = exp.docs;
    docs.target = "_blank";
    docs.rel = "noreferrer";
    docs.textContent = "Documentation";
    body.appendChild(docs);
  }

  body.appendChild(instancesSection(exp));

  const btn = document.createElement("button");
  btn.className = "btn primary";
  btn.textContent = "Bind";
  btn.addEventListener("click", () => bind(exp, btn));

  row.appendChild(body);
  row.appendChild(btn);
  return row;
}

// instancesSection is the per-catalog-item "what is actually running" panel:
// the provider objects consumers synced under this export, loaded on expand.
function instancesSection(exp) {
  const details = document.createElement("details");
  details.className = "instances";
  const summary = document.createElement("summary");
  summary.textContent = "Synced instances";
  const list = document.createElement("div");
  list.className = "instances-list";
  details.append(summary, list);

  details.addEventListener("toggle", async () => {
    if (!details.open) return;
    list.textContent = "Loading…";
    try {
      const res = await getJSON("/api/catalog/" + encodeURIComponent(exp.name) + "/instances");
      renderInstances(list, summary, res.instances || []);
    } catch (e) {
      list.textContent = e.unauthenticated ? "" : "Unavailable: " + (e.message || e);
      if (e.unauthenticated) toLogin();
    }
  });
  return details;
}

function renderInstances(list, summary, instances) {
  summary.textContent = instances.length === 1
    ? "1 synced instance"
    : instances.length + " synced instances";
  list.textContent = "";
  if (instances.length === 0) {
    const p = document.createElement("p");
    p.className = "muted small";
    p.textContent = "Nothing synced under this service yet.";
    list.appendChild(p);
    return;
  }
  for (const inst of instances) {
    const row = document.createElement("div");
    row.className = "instance-row";

    const obj = document.createElement("span");
    obj.className = "mono instance-name";
    obj.textContent = (inst.namespace ? inst.namespace + "/" : "") + inst.name;

    const apiChip = document.createElement("span");
    apiChip.className = "api-name" + (inst.related ? " related-chip" : "");
    apiChip.textContent = inst.related
      ? (inst.direction === "FromConsumer" ? "⇧ " : "⇩ ") + inst.api
      : inst.api;

    const who = document.createElement("span");
    who.className = "instance-who muted small";
    const cluster = inst.clusterUID ? "cluster " + inst.clusterUID.slice(0, 13) : "";
    const parts = [inst.identity, cluster];
    if (inst.related && inst.direction === "FromProvider") {
      // The provider original: its copies live on the consumers, which this
      // side cannot see — say what it is, not who wrote it.
      parts.unshift("delivered to bound clusters");
    }
    parts.push(ago(inst.createdAt));
    who.textContent = parts.filter(Boolean).join(" · ");
    if (inst.clusterUID) who.title = inst.clusterUID;

    row.append(obj, apiChip, who);
    list.appendChild(row);
  }
}

async function bind(exp, btn) {
  btn.disabled = true;
  btn.textContent = "Provisioning…";
  try {
    const res = await getJSON("/api/bind", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ export: exp.name }),
    });
    openTicket(exp, res);
  } catch (e) {
    if (e.unauthenticated) {
      toLogin();
    } else {
      alert("Bind failed: " + (e.message || e));
    }
  } finally {
    btn.disabled = false;
    btn.textContent = "Bind";
  }
}

function openTicket(exp, res) {
  const url = new URL(res.pickupURL, location.origin).toString();
  $("ticket-title").textContent = exp.title || exp.name;
  $("dl").href = url;
  $("dl").setAttribute("download", res.grant + ".yaml");
  $("ticket-expired").hidden = true;
  $("dl").removeAttribute("aria-disabled");

  const curl = "curl -fsS " + url + " | kubectl apply -f -";
  const cli = "kubectl bind export " + exp.name + " --server " + location.origin;
  $("cmd-preview").textContent = "# pick up once, apply, done\n" + curl;
  $("copy-curl").onclick = () => copy(curl, $("copy-curl"));
  $("copy-cli").onclick = () => copy(cli, $("copy-cli"));

  startCountdown(new Date(res.expiresAt));

  const applySection = $("browser-apply");
  applySection.hidden = !provider.applyEnabled;
  if (provider.applyEnabled) {
    $("apply-result").hidden = true;
    $("apply-btn").onclick = () => browserApply(exp);
  }

  $("ticket").showModal();
}

function startCountdown(expiry) {
  clearInterval(countdownTimer);
  const tick = () => {
    const left = Math.floor((expiry - Date.now()) / 1000);
    if (left <= 0) {
      clearInterval(countdownTimer);
      $("countdown").textContent = "0:00";
      $("ticket-expired").hidden = false;
      $("dl").setAttribute("aria-disabled", "true");
      return;
    }
    $("countdown").textContent = Math.floor(left / 60) + ":" + String(left % 60).padStart(2, "0");
  };
  tick();
  countdownTimer = setInterval(tick, 1000);
}

async function browserApply(exp) {
  const kubeconfig = $("apply-kubeconfig").value.trim();
  if (!kubeconfig) {
    alert("Paste a consumer-cluster kubeconfig first.");
    return;
  }
  const btn = $("apply-btn");
  btn.disabled = true;
  btn.textContent = "Applying…";
  try {
    const res = await getJSON("/api/apply", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        export: exp.name,
        kubeconfig: btoa(kubeconfig),
        installKonnector: $("apply-konnector").checked,
      }),
    });
    const out = $("apply-result");
    out.textContent = "applied:\n" + res.applied.map((a) => "  " + a).join("\n");
    out.hidden = false;
  } catch (e) {
    alert("Apply failed: " + (e.message || e));
  } finally {
    btn.disabled = false;
    btn.textContent = "Apply to cluster";
  }
}

// ---------------------------------------------------------------- clusters --

function switchView(name) {
  $("nav-catalog").classList.toggle("active", name === "catalog");
  $("nav-clusters").classList.toggle("active", name === "clusters");
  $("pane-catalog").hidden = name !== "catalog";
  $("pane-clusters").hidden = name !== "clusters";
  if (name === "clusters") loadClusters();
}

async function loadClusters() {
  try {
    renderClusters(await getJSON("/api/clusters"));
  } catch (e) {
    if (e.unauthenticated) {
      toLogin();
      return;
    }
    $("clusters-count").textContent = "unavailable: " + (e.message || e);
  }
}

function ago(iso) {
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(iso)) / 1000));
  if (seconds < 90) return seconds + "s ago";
  if (seconds < 5400) return Math.round(seconds / 60) + "m ago";
  return Math.round(seconds / 3600) + "h ago";
}

function renderClusters(data) {
  const root = $("clusters");
  root.textContent = "";
  const clusters = data.clusters || [];
  $("clusters-count").textContent =
    clusters.length === 1 ? "1 cluster" : clusters.length + " clusters";

  if (clusters.length === 0) {
    const p = document.createElement("p");
    p.className = "muted";
    p.textContent = "No consumer cluster has connected yet. Bind a service and apply its bundle — the konnector's heartbeat shows up here.";
    root.appendChild(p);
  }

  for (const cluster of clusters) {
    const card = document.createElement("div");
    card.className = "cluster";

    const head = document.createElement("div");
    head.className = "cluster-head";
    const dot = document.createElement("span");
    dot.className = "live-dot" + (cluster.live ? "" : " stale");
    dot.title = cluster.live ? "heartbeat current" : "heartbeat stale";
    const uid = document.createElement("span");
    uid.className = "cluster-uid";
    uid.textContent = "cluster " + cluster.uid.slice(0, 13);
    uid.title = cluster.uid;
    const seen = document.createElement("span");
    seen.className = "cluster-seen";
    seen.textContent = (cluster.live ? "heartbeat " : "last seen ") + ago(cluster.lastHeartbeat);
    head.append(dot, uid, seen);
    card.appendChild(head);

    for (const b of cluster.bindings || []) {
      const row = document.createElement("div");
      row.className = "cluster-grant";

      const exp = document.createElement("span");
      exp.className = "cluster-grant-export";
      exp.textContent = b.export;
      exp.title = "grant " + b.grant;

      const identity = document.createElement("span");
      identity.className = "cluster-grant-identity";
      identity.textContent = b.identity || b.subject || "";

      const apis = document.createElement("span");
      apis.className = "cluster-grant-apis";
      for (const a of b.apis || []) {
        const chip = document.createElement("span");
        chip.className = "api-name";
        chip.append(a.name + " · ");
        const count = document.createElement("span");
        count.className = "synced-count";
        count.textContent = a.syncedCount + " synced";
        chip.appendChild(count);
        apis.appendChild(chip);
      }
      row.append(exp, identity, apis);
      card.appendChild(row);
    }
    root.appendChild(card);
  }

  if ((data.pending || []).length > 0) {
    const title = document.createElement("div");
    title.className = "pending-title";
    title.textContent = "bound, never connected";
    root.appendChild(title);
    for (const p of data.pending) {
      const row = document.createElement("div");
      row.className = "pending-row";
      row.textContent = p.export + " — " + (p.identity || p.grant) + " (bound " + ago(p.createdAt) + ", bundle never applied)";
      root.appendChild(row);
    }
  }
}

$("nav-catalog").addEventListener("click", () => switchView("catalog"));
$("nav-clusters").addEventListener("click", () => switchView("clusters"));

// ------------------------------------------------------- connect a cluster --

function openConnect() {
  const cmd = "curl -fsS " + location.origin + "/api/konnector | kubectl apply -f -";
  $("connect-cmd").textContent = cmd;
  $("connect-copy").onclick = () => copy(cmd, $("connect-copy"));

  const applySection = $("connect-apply");
  applySection.hidden = !provider.applyEnabled;
  if (provider.applyEnabled) {
    $("connect-apply-result").hidden = true;
    $("connect-apply-btn").onclick = connectApply;
  }
  $("connect").showModal();
}

async function connectApply() {
  const kubeconfig = $("connect-kubeconfig").value.trim();
  if (!kubeconfig) {
    alert("Paste a consumer-cluster kubeconfig first.");
    return;
  }
  const btn = $("connect-apply-btn");
  btn.disabled = true;
  btn.textContent = "Installing…";
  try {
    const res = await getJSON("/api/apply", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ kubeconfig: btoa(kubeconfig), installKonnector: true }),
    });
    const out = $("connect-apply-result");
    out.textContent = "installed:\n" + res.applied.map((a) => "  " + a).join("\n") +
      "\n\nNow bind a service from the Catalog.";
    out.hidden = false;
  } catch (e) {
    alert("Install failed: " + (e.message || e));
  } finally {
    btn.disabled = false;
    btn.textContent = "Install konnector";
  }
}

$("connect-btn").addEventListener("click", openConnect);
$("connect-close").addEventListener("click", () => $("connect").close());

async function copy(text, btn) {
  try {
    await navigator.clipboard.writeText(text);
    const prev = btn.textContent;
    btn.textContent = "Copied";
    setTimeout(() => (btn.textContent = prev), 1200);
  } catch {
    prompt("Copy this:", text);
  }
}

$("ticket-close").addEventListener("click", () => {
  clearInterval(countdownTimer);
  $("ticket").close();
});

init();
