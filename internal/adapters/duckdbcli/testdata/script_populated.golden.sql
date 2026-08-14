-- agent-logs-extractor: generated DuckDB export script. Do not edit.
-- Reproduce by hand:
--   cd <canonical store root> && duckdb -init /dev/null -batch -bail /tmp/snapshot.duckdb < script.sql
-- The store root is the process working directory, never a literal in this
-- script, so no store path can be glob-expanded or quote-injected here.

SET autoinstall_known_extensions = false;

BEGIN TRANSACTION;

CREATE TEMP TABLE _docs AS
SELECT *
FROM read_json(
        'sessions/*/*.json',
        format = 'newline_delimited',
        maximum_object_size = 67108864,
        columns = {
            'session': 'STRUCT(session_id VARCHAR, vendor VARCHAR, project_path VARCHAR, project_name VARCHAR, started_at TIMESTAMPTZ, ended_at TIMESTAMPTZ, git_branch VARCHAR, vendor_version VARCHAR, source_path VARCHAR)',
            'messages': 'STRUCT(message_id VARCHAR, session_id VARCHAR, seq INTEGER, parent_message_id VARCHAR, role VARCHAR, created_at TIMESTAMPTZ, text VARCHAR, model VARCHAR, raw JSON)[]',
            'tool_calls': 'STRUCT(tool_call_id VARCHAR, session_id VARCHAR, message_id VARCHAR, seq INTEGER, tool_name VARCHAR, arguments JSON, output VARCHAR, status VARCHAR, created_at TIMESTAMPTZ)[]'
        }
     );

CREATE TABLE sessions AS
SELECT
    d.session.session_id     AS session_id,
    d.session.vendor         AS vendor,
    d.session.project_path   AS project_path,
    d.session.project_name   AS project_name,
    d.session.started_at     AS started_at,
    d.session.ended_at       AS ended_at,
    d.session.git_branch     AS git_branch,
    d.session.vendor_version AS vendor_version,
    d.session.source_path    AS source_path
FROM _docs d;

CREATE TABLE messages AS
WITH exploded AS (
    SELECT d.session AS s, UNNEST(d.messages) AS m
    FROM _docs d
    WHERE d.messages IS NOT NULL
)
SELECT
    m.message_id AS message_id, m.session_id AS session_id, m.seq AS seq,
    m.parent_message_id AS parent_message_id, m.role AS role,
    m.created_at AS created_at, m.text AS text, m.model AS model, m.raw AS raw,
    s.vendor AS vendor, s.project_name AS project_name, s.project_path AS project_path
FROM exploded;

CREATE TABLE tool_calls AS
WITH exploded AS (
    SELECT d.session AS s, UNNEST(d.tool_calls) AS t
    FROM _docs d
    WHERE d.tool_calls IS NOT NULL
)
SELECT
    t.tool_call_id AS tool_call_id, t.session_id AS session_id,
    t.message_id AS message_id, t.seq AS seq, t.tool_name AS tool_name,
    t.arguments AS arguments, t.output AS output, t.status AS status,
    t.created_at AS created_at,
    s.vendor AS vendor, s.project_name AS project_name, s.project_path AS project_path
FROM exploded;

COMMIT;
CHECKPOINT;
