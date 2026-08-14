package duckdbcli

// The README's "Querying" cookbook, byte-identical here and in README.md.
// They are a tested contract (D11): TestCookbookQueriesMatchTheREADME
// parses the ```sql fences out of README.md and fails the build the moment
// either copy drifts from the other, and TestCookbookQueries (integration)
// runs these same constants against a real export of the fixtures.
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
