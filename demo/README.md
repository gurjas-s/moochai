# Mooch.ai demo

These scripts simulate a cluster of 20 to 100 users on one computer, so you can show the analytics page without a room of computers.

## Start central with analytics

```sh
cd central
make start                  # TimescaleDB, and central with analytics
```

Central also listens on `127.0.0.1:8080`, so the scripts connect with no options.
You do not need a special mode. The same central also serves the nodes on the tailnet.
For `simulate-fast.py`, use `make start FLAGS=-plain > /dev/null`.

Caution: the fake users are real nodes for central. During a demo, central can send requests from tailnet users to fake users.
Those requests get fake answers. Do not run a demo while other people use the cluster.

Open <http://127.0.0.1:3000/analytics>. The page updates every half second.

## Scripts

Run the scripts from the repository root.

| Script | Use |
|--------|-----|
| `python3 demo/simulate.py` | 20 users with realistic speeds for 5 minutes. A few requests per second. Some users go offline for a short time. |
| `python3 demo/simulate-fast.py` | 50 users at about 100 requests per second for 2 minutes. Use `--users` and `--rps` to change the size and the rate. |
| `demo/clear-db.sh` | Delete all analytics data, so the next demo starts empty. Add `-y` to skip the question. |

Both simulators accept `--minutes`, `--users` (at most 100), `--seed`, and `--central`. Run a script with `--help` to see all options.
The first 20 users are fixed. The script generates the other users with the same mix of givers and takers. The same `--seed` gives the same users.

## Notes

- Each user gets its own loopback IP (`127.0.0.10` and up), so central can tell the users apart.
  On macOS, the simulators ask for `sudo` once to add these addresses, and remove them at the end.
- With `--single-ip`, all users use `127.0.0.1`. The script then needs no `sudo`, but central counts all requests for one requester.
- The simulators stop all fake users at the end of the run, on Ctrl+C, on `kill`, and when you close the terminal.
  After `kill -9`, the worker processes of `simulate-fast.py` see that the parent stopped, and stop in less than 1 second.
  Central removes the users from its node list about 45 seconds later.
- For `simulate-fast.py`, start central with `-plain > /dev/null`. The dashboard draws each request, and that uses CPU at a high rate.
- `clear-db.sh` uses `MOOCH_DB_URL` if it is set. Else it uses the local database from `make db-up`.
