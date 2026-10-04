-- Central applies each block at start. A blank line separates the blocks.
-- Each block is safe to apply again. Continuous aggregates cannot run in a transaction, so central sends each block alone.

CREATE EXTENSION IF NOT EXISTS timescaledb

CREATE TABLE IF NOT EXISTS requests (
  time              timestamptz      NOT NULL,
  requester         text             NOT NULL,
  node              text             NOT NULL,
  model             text             NOT NULL,
  path              text             NOT NULL,
  status            int              NOT NULL,
  duration_ms       double precision NOT NULL,
  bytes_out         bigint           NOT NULL,
  prompt_tokens     int,
  completion_tokens int
)

SELECT create_hypertable('requests', by_range('time', INTERVAL '1 day'), if_not_exists => TRUE)

CREATE INDEX IF NOT EXISTS requests_node_time ON requests (node, time DESC)

DO $$ BEGIN
  IF NOT (SELECT compression_enabled FROM timescaledb_information.hypertables WHERE hypertable_name = 'requests') THEN
    ALTER TABLE requests SET (timescaledb.compress, timescaledb.compress_segmentby = 'node', timescaledb.compress_orderby = 'time DESC');
  END IF;
END $$

SELECT add_compression_policy('requests', INTERVAL '7 days', if_not_exists => TRUE)

SELECT add_retention_policy('requests', INTERVAL '90 days', if_not_exists => TRUE)

CREATE TABLE IF NOT EXISTS heartbeats (
  time   timestamptz NOT NULL,
  node   text        NOT NULL,
  models int         NOT NULL
)

SELECT create_hypertable('heartbeats', by_range('time', INTERVAL '1 day'), if_not_exists => TRUE)

SELECT add_retention_policy('heartbeats', INTERVAL '30 days', if_not_exists => TRUE)

CREATE MATERIALIZED VIEW IF NOT EXISTS usage_hourly
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket(INTERVAL '1 hour', time) AS bucket,
       requester, node, model,
       count(*)                                                       AS requests,
       count(*) FILTER (WHERE status < 400)                           AS ok,
       sum(duration_ms)                                               AS duration_ms,
       sum(coalesce(prompt_tokens, 0) + coalesce(completion_tokens, 0)) AS tokens
FROM requests
GROUP BY bucket, requester, node, model
WITH NO DATA

SELECT add_continuous_aggregate_policy('usage_hourly',
  start_offset => INTERVAL '3 hours', end_offset => INTERVAL '1 minute',
  schedule_interval => INTERVAL '1 minute', if_not_exists => TRUE)
