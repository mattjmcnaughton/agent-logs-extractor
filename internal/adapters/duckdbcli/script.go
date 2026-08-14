package duckdbcli

import "fmt"

// The three read_json column type strings, one per top-level SessionDoc
// field (internal/core/model.SessionDoc). These are the single source of
// truth for the export schema's column set: TestScriptColumnsCoverEveryModelJSONTag
// reflects over the model structs and asserts every json tag appears here,
// so an added model field that is forgotten here fails a unit test rather
// than silently vanishing from the export.
//
// TIMESTAMPTZ, not TIMESTAMP (TDD decision refinement, #9): with plain
// TIMESTAMP, `now() - INTERVAL 1 DAY` forces an implicit cast through the
// session's local timezone and silently shifts every comparison by that
// offset — wrong answers for exactly the two README cookbook queries that
// filter on time. TIMESTAMPTZ keeps every comparison in UTC.
const (
	sessionStructType  = `STRUCT(session_id VARCHAR, vendor VARCHAR, project_path VARCHAR, project_name VARCHAR, started_at TIMESTAMPTZ, ended_at TIMESTAMPTZ, git_branch VARCHAR, vendor_version VARCHAR, source_path VARCHAR)`
	messageStructType  = `STRUCT(message_id VARCHAR, session_id VARCHAR, seq INTEGER, parent_message_id VARCHAR, role VARCHAR, created_at TIMESTAMPTZ, text VARCHAR, model VARCHAR, raw JSON)[]`
	toolCallStructType = `STRUCT(tool_call_id VARCHAR, session_id VARCHAR, message_id VARCHAR, seq INTEGER, tool_name VARCHAR, arguments JSON, output VARCHAR, status VARCHAR, created_at TIMESTAMPTZ)[]`

	// storeGlob is relative, never a literal absolute path: the adapter
	// runs duckdb with the store root as cmd.Dir, so the store root itself
	// never appears in generated SQL (D2) — no quote-escaping story, and no
	// risk that a store root containing '*', '?', or '[' gets glob-expanded
	// by DuckDB into the wrong files.
	storeGlob = `sessions/*/*.json`

	// maxObjectSize caps read_json's per-record buffer at 64MiB, comfortably
	// above any real session doc, so a single oversized record errors
	// loudly instead of read_json silently truncating it.
	maxObjectSize = 67108864
)

// populatedHeader is the header comment plus the read_json-backed _docs CTE,
// used when the store has at least one session doc.
const populatedHeader = `-- agent-logs-extractor: generated DuckDB export script. Do not edit.
-- Reproduce by hand:
--   cd <canonical store root> && duckdb -init /dev/null -batch -bail /tmp/snapshot.duckdb < script.sql
-- The store root is the process working directory, never a literal in this
-- script, so no store path can be glob-expanded or quote-injected here.

SET autoinstall_known_extensions = false;

BEGIN TRANSACTION;

CREATE TEMP TABLE _docs AS
SELECT *
FROM read_json(
        '%s',
        format = 'newline_delimited',
        maximum_object_size = %d,
        columns = {
            'session': '%s',
            'messages': '%s',
            'tool_calls': '%s'
        }
     );

`

// emptyHeader declares _docs with the same three column types but no
// read_json call: read_json raises "IO Error: No files found that match the
// pattern" on a zero-match glob, and no version-independent SQL workaround
// exists, so an empty store is branched in Go (storeHasDocs), not SQL.
const emptyHeader = `-- agent-logs-extractor: generated DuckDB export script. Do not edit.
-- Reproduce by hand:
--   cd <canonical store root> && duckdb -init /dev/null -batch -bail /tmp/snapshot.duckdb < script.sql
-- The store root is the process working directory, never a literal in this
-- script, so no store path can be glob-expanded or quote-injected here.
-- This store has no session docs (sessions/ absent or empty), so _docs is
-- declared empty rather than read via read_json: read_json errors on a
-- glob that matches zero files, and no version-independent SQL workaround
-- exists (see storeHasDocs in duckdbcli.go).

SET autoinstall_known_extensions = false;

BEGIN TRANSACTION;

CREATE TEMP TABLE _docs (
    session %s,
    messages %s,
    tool_calls %s
);

`

// scriptTail builds the three relations from _docs and is byte-identical
// regardless of hasDocs by construction: Script appends this same constant
// after either header, so the two branches can never declare different
// columns. Session fields are denormalized (vendor/project_name/project_path) onto
// messages and tool_calls (TDD core decision 5) so the README cookbook
// queries need no joins. `messages IS NOT NULL` / `tool_calls IS NOT NULL`
// excludes a doc whose field is JSON null; UNNEST of an empty (non-null)
// list contributes no rows on its own, so both zero-row shapes of "this doc
// has no messages" need no separate branch.
const scriptTail = `CREATE TABLE sessions AS
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
`

// Script renders the export script. It is a pure function of hasDocs — the
// store root is NOT a parameter; the adapter runs duckdb with the store
// root as its working directory (D2), so Script never sees, and can never
// embed, a store path. Two possible outputs, both pinned by golden files
// (testdata/script_populated.golden.sql, testdata/script_empty.golden.sql).
func Script(hasDocs bool) string {
	var header string
	if hasDocs {
		header = fmt.Sprintf(populatedHeader, storeGlob, maxObjectSize, sessionStructType, messageStructType, toolCallStructType)
	} else {
		header = fmt.Sprintf(emptyHeader, sessionStructType, messageStructType, toolCallStructType)
	}
	return header + scriptTail
}
