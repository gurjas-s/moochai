#!/bin/sh
# Delete all analytics rows (requests, heartbeats, and the hourly aggregate), so the next demo starts empty.
# Central can keep running. The tables stay, so central does not need a restart.
#
#   demo/clear-db.sh        # asks before it deletes
#   demo/clear-db.sh -y     # no question
#
# It uses MOOCH_DB_URL if it is set, else the local database from `make db-up`.
set -eu
url=${MOOCH_DB_URL:-postgres://postgres:mooch@127.0.0.1:5432/postgres}
host=$(printf '%s' "$url" | sed -E 's#^[a-z]+://([^@]*@)?([^/:?]+).*#\2#')

if [ "${1:-}" != "-y" ]; then
  printf 'Delete all analytics data in the database on %s? [y/N] ' "$host"
  read -r answer
  [ "$answer" = "y" ] || [ "$answer" = "Y" ] || { echo "Nothing deleted."; exit 1; }
fi

# Each -c runs alone, because refresh_continuous_aggregate cannot run in a transaction.
sql_truncate="TRUNCATE requests, heartbeats"
sql_refresh="CALL refresh_continuous_aggregate('usage_hourly', NULL, NULL)"
if command -v psql >/dev/null 2>&1; then
  psql "$url" -q -v ON_ERROR_STOP=1 -c "$sql_truncate" -c "$sql_refresh"
else
  # Without psql on this computer, use the psql in the container that `make db-up` starts.
  docker exec mooch-db psql -U postgres -q -v ON_ERROR_STOP=1 -c "$sql_truncate" -c "$sql_refresh"
fi
echo "Deleted all analytics data. The page shows an empty cluster at the next update."
