#!/usr/bin/env node
// CI gate: fails if adapters/ is out of date with the canonical skill.
// Regenerates the adapters into a temp dir via build-adapters.js and diffs
// against the committed copies.
'use strict';

const { execFileSync } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

const repoRoot = path.join(__dirname, '..');
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'greybeard-adapters-'));

execFileSync(process.execPath, [path.join(__dirname, 'build-adapters.js'), tmp], {
  stdio: ['ignore', 'ignore', 'inherit'],
});

function listFiles(dir, base = dir) {
  if (!fs.existsSync(dir)) return [];
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) => {
    const p = path.join(dir, e.name);
    return e.isDirectory() ? listFiles(p, base) : [path.relative(base, p)];
  });
}

const expectedRoot = path.join(tmp, 'adapters');
const actualRoot = path.join(repoRoot, 'adapters');
const expected = new Set(listFiles(expectedRoot));
const actual = new Set(listFiles(actualRoot));
const problems = [];

for (const f of expected) {
  if (!actual.has(f)) {
    problems.push(`missing: adapters/${f}`);
  } else if (
    fs.readFileSync(path.join(expectedRoot, f), 'utf8') !==
    fs.readFileSync(path.join(actualRoot, f), 'utf8')
  ) {
    problems.push(`out of date: adapters/${f}`);
  }
}
for (const f of actual) {
  if (!expected.has(f)) problems.push(`unexpected (not generated): adapters/${f}`);
}

fs.rmSync(tmp, { recursive: true, force: true });

if (problems.length) {
  console.error('adapters/ is out of sync with skills/greybeard/SKILL.md:\n');
  for (const p of problems) console.error('  ' + p);
  console.error('\nRun `node scripts/build-adapters.js` and commit the result.');
  process.exit(1);
}
console.log('adapters/ is up to date.');
