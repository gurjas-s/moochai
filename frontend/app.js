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
  const board = await loadJSON(["./leaderboard.json", "./public/leaderboard.example.json"]);
  renderBoard(board ? board.entries : []);
}

main();
