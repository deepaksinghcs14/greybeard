---
name: greybeard-audit
description: Full-graph sanity check — repos with no extracted data, and edges that look stale against the last build time. One-shot report, changes nothing.
---

Call the greybeard MCP server's `audit_graph` tool. Report registered repos with zero extracted nodes (extraction likely failed or repo has no parseable manifests), and any edges older than the configured staleness threshold. This is diagnostic only — never modify the graph or any repo as part of this command.
