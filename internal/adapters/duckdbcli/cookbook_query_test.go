package duckdbcli_test

// The README's "Querying" cookbook, byte-identical here and in README.md.
// They are a tested contract (D11): TestCookbookQueriesMatchTheREADME
// parses the ```sql fences out of README.md and fails the build the moment
// either copy drifts from the other, and TestCookbookQueries (integration)
// runs these same constants against a real export of the fixtures.
//
// These live in a no-build-tag _test.go file, not the production package,
// on purpose: nothing in production ever reads them (they exist purely as
// a tested anchor for the README's documentation), so keeping them here
// shrinks duckdbcli's public API to New/Name/Export/SinkName/the two
// sentinel errors — the same pattern duckdbtime_test.go already
// established for parseDuckDBTimestamp. Both the no-build-tag
// TestCookbookQueriesMatchTheREADME (cookbook_test.go) and the
// //go:build integration TestCookbookQueries (cookbook_integration_test.go)
// are compiled into the same duckdbcli_test package, so both see these
// unqualified.
const (
	CookbookQuery1 = `SELECT created_at, text
FROM messages
WHERE role = 'user'
  AND project_name = 'fetch-context'
  AND created_at > now() - INTERVAL 1 DAY
ORDER BY created_at;`

	CookbookQuery2 = `SELECT created_at, vendor, project_name, arguments
FROM tool_calls
WHERE tool_name = 'Bash'
  AND arguments LIKE '%git push%'
  AND created_at > now() - INTERVAL 7 DAY;`

	CookbookQuery3 = `SELECT project_name, vendor, count(*) AS sessions, max(ended_at) AS last_active
FROM sessions
GROUP BY ALL
ORDER BY sessions DESC;`
)
