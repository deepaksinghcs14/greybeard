---
name: greybeard-build
description: Run extraction across all registered repos and populate the graph. Safe to rerun any time — it's a full rebuild, not incremental.
---

Call the greybeard MCP server's `build_graph` tool. Report the number of repos processed, nodes/edges created, and any repos that failed extraction (missing manifests, unparseable specs) so the user knows what's not covered yet.
