---
name: greybeard-query
description: Ask the graph what's related to a repo, endpoint, schema, or exported symbol, directly in chat. Use for ad-hoc questions outside of an active code change.
---

Take the user's target (repo name, endpoint, schema, or exported function/type/class name) and call the appropriate greybeard MCP tool (`get_related_repos`, `get_callers_of`, or `get_schema_dependents`). Summarize the result in plain language, per the interpretation guidance in the greybeard skill — don't dump raw JSON.
