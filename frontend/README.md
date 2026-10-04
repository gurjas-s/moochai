# Frontend — mooch.tech

Static leaderboard. No Go code. No secrets.

## Files

- `index.html` — description, network counts, leaderboard.
- `app.js` — loads snapshots. Sends no data.
- `styles.css` — styles.
- `public/status.example.json` — sample counts.
- `public/leaderboard.example.json` — sample board.

## Live data contract

Central exports two public files every 15 seconds:

- `status.json` — status, uptime, models, nodes, feed.
- `leaderboard.json` — alias, models, served, ok_pct, p50_ms, uptime.

The site tries `./status.json` first, then `./public/status.example.json`.
Same rule applies to the board.

## Anonymize rules

Warning: never publish tailnet data. Strip these fields before export:

- `tailscale_ip`
- `listen_addr`
- `endpoint`
- `node_id` and `name`
- prompt text and response text

Replace `node_id` with `alias` like `node-a3f9`.
Use a hash of `node_id`. Keep the same alias for each node.
Show counts only. Show no raw prompts.

## Preview locally

Serve the folder over HTTP. Use one command:

```sh
cd frontend
python3 -m http.server 8000
```

Open `http://127.0.0.1:8000`.

## Deploy

Upload the folder to static hosting. Point `mooch.tech` to the host.
Copy fresh `status.json` and `leaderboard.json` next to `index.html`.
