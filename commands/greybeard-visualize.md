---
name: greybeard-visualize
description: Open the interactive cross-repo graph in the browser — force-directed layout, zoom/pan, click a node for its endpoints, schemas, and connections. Read-only view of the live graph.
---

Call the greybeard MCP server's `visualize_graph` tool and give the user the returned URL (it also opens in their default browser automatically). If the tool reports the server was already running, just share the URL. Remind them the page re-reads the graph on every reload, so after a build they can simply refresh.
