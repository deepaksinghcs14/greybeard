#!/usr/bin/env node
// Regenerates adapters/ from the canonical skill (skills/greybeard/SKILL.md).
// Run after any edit to the canonical skill; CI enforces freshness via
// check-adapter-copies.js. Optional argv[2] = alternate output root (used by
// the checker to regenerate into a temp dir).
'use strict';

const fs = require('fs');
const path = require('path');

const repoRoot = path.join(__dirname, '..');
const outRoot = process.argv[2] ? path.resolve(process.argv[2]) : repoRoot;

const skillDir = path.join(repoRoot, 'skills', 'greybeard');
const src = fs.readFileSync(path.join(skillDir, 'SKILL.md'), 'utf8');

const m = src.match(/^---\r?\n([\s\S]*?)\r?\n---\r?\n([\s\S]*)$/);
if (!m) throw new Error('SKILL.md: could not parse frontmatter');
const frontmatter = m[1];
const body = m[2].trim();

const descMatch = frontmatter.match(/^description:\s*(.+)$/m);
if (!descMatch) throw new Error('SKILL.md: no description in frontmatter');
const description = descMatch[1].trim();

function write(rel, content) {
  const p = path.join(outRoot, rel);
  fs.mkdirSync(path.dirname(p), { recursive: true });
  fs.writeFileSync(p, content);
  console.log('wrote', rel);
}

// --- Codex adapter -----------------------------------------------------------
// Same SKILL.md shape as the canonical skill: Codex Agent Skills read
// name/description frontmatter exactly like Claude Code (verified against
// developers.openai.com/codex/skills, mid-2026). Install by copying this
// directory to .agents/skills/greybeard (repo) or ~/.agents/skills/greybeard
// (user); older Codex builds used ~/.codex/skills/.
write(
  path.join('adapters', 'codex', 'greybeard', 'SKILL.md'),
  `---\nname: greybeard\ndescription: ${description}\n---\n\n${body}\n`
);
// Ship the reference docs alongside, so the body's references/*.md links resolve.
for (const ref of fs.readdirSync(path.join(skillDir, 'references'))) {
  write(
    path.join('adapters', 'codex', 'greybeard', 'references', ref),
    fs.readFileSync(path.join(skillDir, 'references', ref), 'utf8')
  );
}

// --- Instruction-only adapter (Cursor, Windsurf, Cline, ...) -----------------
// No skill/plugin system on these hosts: flatten to an always-on ruleset with
// no frontmatter. Every references/*.md pointer is redirected to the README
// (those files don't ship with a pasted ruleset), and the session-start-hook
// section is rewritten — these hosts have no hooks, so indexing is manual.
const README = 'the greybeard README (https://github.com/deepaksinghcs14/greybeard)';
const flattened = body
  .replace(
    /^See `references\/mcp-tools\.md`.*$/m,
    `See ${README} for full tool signatures and the node/edge model if you need to reason about what the graph does and doesn't capture.`
  )
  .replace(/\(see `references\/mcp-tools\.md`[^)]*\)/g, `(see ${README} for exact signatures)`)
  .replace(/See `references\/graph-schema\.md`[^.]*\./g, `See ${README} for the discovery/freshness rules.`)
  .replace(
    /## You don't need to trigger indexing yourself\n[\s\S]*?(?=\n## )/,
    `## Keeping the graph fresh

This host has no session hooks, so indexing does not happen automatically here. If a query returns empty for a repo that plausibly has cross-repo ties, its extraction may be missing or stale — suggest running \`greybeard build\` (full rebuild) or \`greybeard check --cwd <repo>\` (fast freshness check) in a terminal rather than asserting the repo has no dependencies.
`
  );
const applyWhen = description.replace(/^Use this skill\s+/i, '');
write(
  path.join('adapters', 'instruction-only', 'greybeard.md'),
  `# greybeard — cross-repo context rules\n\nApply these rules ${applyWhen}\n\n${flattened}\n`
);
