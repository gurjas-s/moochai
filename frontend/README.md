# Frontend: mooch.tech

Static site. No Go code. No secrets.

## Files

- `index.html`: setup guide, live network, leaderboard.
- `app.js`: loads snapshots. Sends no data.
- `styles.css`: styles.
- `public/status.example.json`: sample live snapshot.
- `public/leaderboard.example.json`: sample board.

## Live data contract

The site reads two files:

- `status.json`: status, uptime, models, nodes, feed.
- `leaderboard.json`: alias, models, served, ok_pct, p50_ms, uptime.

The site tries `./status.json` first, then `./public/status.example.json`.
The same rule applies to the board.

Central serves `GET /leaderboard.json` for the last 24 hours.
This route needs analytics (`MOOCH_DB_URL`). Read [`server/README.md`](../server/README.md#analytics).
Central does not make `status.json`.

## Anonymize rules

Warning: never publish tailnet data. Remove these fields before export:

- `tailscale_ip`
- `listen_addr`
- `endpoint`
- `node_id` and `name`
- prompt text and response text

Replace `node_id` with `alias` like `node-a3f9`.
Use a hash of `node_id`. Keep the same alias for each node.
Show counts only. Show no raw prompts.

Caution: central uses the node name as `alias` in `/leaderboard.json`.
Change the names before you publish the file.

## Preview locally

Serve the folder over HTTP:

```sh
cd frontend
python3 -m http.server 8000
```

Open `http://127.0.0.1:8000`.

## Deploy

Upload the folder to static hosting. Point `mooch.tech` to the host.
Copy fresh `status.json` and `leaderboard.json` next to `index.html`.
