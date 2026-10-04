// Frontend reads public snapshots. It sends no data.
// It loads status.json and leaderboard.json, else sample files.
async function loadJSON(paths) {
  for (const p of paths) {
    try {
      const r = await fetch(p, { cache: "no-store" });
      if (r.ok) return await r.json();
    } catch (e) {
      // Try the next path.
    }
  }
  return null;
}

function text(el, v) {
  document.getElementById(el).textContent = v;
}

function renderModels(models, filter) {
  const tb = document.querySelector("#modelTable tbody");
  const q = (filter || "").toLowerCase();
  const rows = (models || []).filter((m) => m.id.toLowerCase().includes(q));
  if (rows.length === 0) {
    tb.innerHTML = '<tr><td colspan="5">No models match.</td></tr>';
    return;
  }
  tb.innerHTML = rows.map((m) =>
    `<tr><td><code>${m.id}</code></td><td>${m.type}</td><td>${m.provider}</td><td>${m.streaming ? "yes" : "no"}</td><td>${m.healthy_nodes}</td></tr>`
  ).join("");
}

function renderNodes(nodes) {
  const tb = document.querySelector("#nodeTable tbody");
  if (!nodes || nodes.length === 0) {
    tb.innerHTML = '<tr><td colspan="4">No nodes live.</td></tr>';
    return;
  }
  tb.innerHTML = nodes.map((n) =>
    `<tr><td><code>${n.alias}</code></td><td>${(n.models || []).join(", ")}</td><td>${n.healthy ? "ready" : "down"}</td><td>${n.seen_ago_s}s ago</td></tr>`
  ).join("");
}

function renderBoard(entries) {
  const tb = document.querySelector("#boardTable tbody");
  if (!entries || entries.length === 0) {
    tb.innerHTML = '<tr><td colspan="7">No entries yet.</td></tr>';
    return;
  }
  const sorted = [...entries].sort((a, b) =>
    (b.served - a.served) || (b.ok_pct - a.ok_pct) || (a.p50_ms - b.p50_ms));
  tb.innerHTML = sorted.map((e, i) =>
    `<tr><td>${i + 1}</td><td><code>${e.alias}</code></td><td>${e.models}</td><td>${e.served}</td><td>${e.ok_pct}</td><td>${e.p50_ms} ms</td><td>${e.uptime}</td></tr>`
  ).join("");
}

async function main() {
  const status = await loadJSON(["./status.json", "./public/status.example.json"]);
  if (!status) {
    text("stStatus", "offline");
    return;
  }
  text("stStatus", status.status || "ok");
  text("stNodes", String(status.nodes_live ?? "—"));
  text("stModels", String(status.models_live ?? "—"));
  text("stUptime", status.uptime || "—");
  text("stUpdated", status.updated_at || "—");
  const search = document.getElementById("modelSearch");
  const draw = () => renderModels(status.models, search.value);
  search.addEventListener("input", draw);
  draw();
  renderNodes(status.nodes);
  document.getElementById("feedOut").textContent = (status.feed || []).join("\n") || "No events.";
  const board = await loadJSON(["./leaderboard.json", "./public/leaderboard.example.json"]);
  renderBoard(board ? board.entries : []);
}

main();
