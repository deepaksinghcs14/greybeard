---
name: greybeard-init
description: Register a root folder with greybeard's local graph store (embedded SQLite — no database setup). Run once, or again if you want to add a new root. Scans for every git repo under the given path — you don't list repos individually.
---

Take the root path the user gives you (default to their common code folder if they don't specify one, e.g. `~/code` or `~/dev` — ask if genuinely ambiguous) and invoke the greybeard MCP server's `init_root` tool with it. That call walks the tree for `.git` directories and registers each one found — report back how many repos were discovered. Remind the user to run `/greybeard-build` next for the first full extraction. New repos added under this root later don't need this command rerun — they're picked up automatically the next time an agent opens them (see the session-start hook).
