#!/usr/bin/env node
// contracts-version-check (HUB-014): every contract document whose content changed since the
// base has a higher version and a contracts/CHANGELOG.md entry (AGENTS.md §4.3).
//
//   node version-check.mjs --base <git ref>     (CI: the PR base; locally: origin/main)
//
// Compares the working tree with the merge base of <ref> and HEAD, so changes that landed on
// the base branch meanwhile are not attributed to this branch. "Content changed" means the
// parsed document differs (comments and formatting alone do not count).
//
// | Document                     | Version field                | Changelog entry must name      |
// | openapi/<name>.v<N>.yaml     | info.version                 | `<name>` <version>             |
// | mcp/hubmcp-tools.json        | version                      | `hubmcp-tools` <version>       |
// | schemas/events/*.json        | hub-public info.version      | `hub-public` <version>         |
//
// A new document needs a changelog entry naming it; a deleted document is an error (a new
// major is a new file, the old one stays: README "Versioning"). Since ADR-0023 (HUB-432)
// schemas/events/ is the only JSON Schema document kind.
import fs from 'node:fs';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { parse as parseYaml } from 'yaml';
import { isDeepStrictEqual } from 'node:util';

const HERE = path.dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2);
const baseRef = args[args.indexOf('--base') + 1];
if (!args.includes('--base') || !baseRef) {
  console.error('usage: node version-check.mjs --base <git ref>');
  process.exit(2);
}
const TOP = execFileSync('git', ['rev-parse', '--show-toplevel'], { cwd: HERE, encoding: 'utf8' }).trim();
// Run git from the top level so every pathspec below is repo-relative.
const git = (...a) => execFileSync('git', a, { cwd: TOP, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
const CONTRACTS = 'contracts';
let mergeBase;
try {
  mergeBase = git('merge-base', baseRef, 'HEAD').trim();
} catch (e) {
  console.error(`cannot find the merge base of '${baseRef}' and HEAD (fetch the base branch with enough history): ${e.stderr || e.message}`);
  process.exit(2);
}

const problems = [];
const SEMVER = /^(\d+)\.(\d+)\.(\d+)$/;
const cmp = (a, b) => {
  const pa = SEMVER.exec(a).slice(1).map(Number);
  const pb = SEMVER.exec(b).slice(1).map(Number);
  for (let i = 0; i < 3; i++) if (pa[i] !== pb[i]) return pa[i] - pb[i];
  return 0;
};
const parse = (file, text) => (file.endsWith('.json') ? JSON.parse(text) : parseYaml(text));
const isDoc = (f) => /^contracts\/(openapi\/[^/]+\.ya?ml|mcp\/hubmcp-tools\.json|schemas\/events\/[^/]+\.json)$/.test(f);

const atBase = (f) => {
  try {
    return parse(f, git('show', `${mergeBase}:${f}`));
  } catch {
    return undefined;
  }
};
const now = (f) => (fs.existsSync(path.join(TOP, f)) ? parse(f, fs.readFileSync(path.join(TOP, f), 'utf8')) : undefined);

const baseFiles = git('ls-tree', '-r', '--name-only', mergeBase, '--', CONTRACTS).split('\n').filter(isDoc);
const listNow = (dir) =>
  fs.readdirSync(path.join(TOP, dir), { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? listNow(`${dir}/${e.name}`) : [`${dir}/${e.name}`]));
const nowFiles = listNow(CONTRACTS).filter(isDoc);
const files = [...new Set([...baseFiles, ...nowFiles])].sort();

// Lines added to contracts/CHANGELOG.md on this branch (committed or not).
const changelogAdded = git('diff', mergeBase, '--', `${CONTRACTS}/CHANGELOG.md`)
  .split('\n')
  .filter((l) => l.startsWith('+') && !l.startsWith('+++'))
  .join('\n');
const hasEntry = (needle) => changelogAdded.includes(needle);

const publicNow = now(`${CONTRACTS}/openapi/hub-public.v1.yaml`);
const publicBase = atBase(`${CONTRACTS}/openapi/hub-public.v1.yaml`);

const report = [];
for (const f of files) {
  const before = atBase(f);
  const after = now(f);
  if (after === undefined && before !== undefined) {
    problems.push(`${f}: deleted. Contract documents are never deleted; a new major is a new file`);
    continue;
  }
  let name;
  let vBefore;
  let vAfter;
  let strip;
  if (f.includes('/openapi/')) {
    name = path.basename(f).replace(/\.v\d+\.ya?ml$/, '');
    vBefore = before?.info?.version;
    vAfter = after?.info?.version;
    strip = (d) => ({ ...d, info: { ...d.info, version: undefined } });
  } else if (f.endsWith('hubmcp-tools.json')) {
    name = 'hubmcp-tools';
    vBefore = before?.version;
    vAfter = after?.version;
    strip = (d) => ({ ...d, version: undefined });
  } else {
    name = 'hub-public';
    vBefore = publicBase?.info?.version;
    vAfter = publicNow?.info?.version;
    strip = (d) => d;
  }

  if (before === undefined) {
    report.push(`${f}: new (${name} ${vAfter ?? ''})`);
    if (!hasEntry(`\`${name}\``)) problems.push(`${f}: new document but no contracts/CHANGELOG.md entry names \`${name}\``);
    continue;
  }
  const contentChanged = !isDeepStrictEqual(strip(before), strip(after));
  // Event schemas carry no version of their own: they only need a bump when they change.
  const versionChanged = vBefore !== vAfter && !f.includes('/schemas/events/');
  if (!contentChanged && !versionChanged) continue;

  const higher = SEMVER.test(String(vAfter)) && SEMVER.test(String(vBefore)) && cmp(vAfter, vBefore) > 0;
  const label = f.includes('/schemas/events/') ? 'hub-public info.version' : 'version';
  report.push(`${f}: ${contentChanged ? 'content changed' : 'version only'}, ${name} ${vBefore} -> ${vAfter}`);
  if (!higher) {
    problems.push(`${f}: ${contentChanged ? 'content changed but ' : ''}${label} went ${vBefore} -> ${vAfter}; it must increase (README "Versioning")`);
    continue;
  }
  const needle = `\`${name}\` ${vAfter}`;
  if (!hasEntry(needle)) problems.push(`${f}: no contracts/CHANGELOG.md entry added for ${needle}`);
}

console.log(`contracts-version-check: base ${baseRef} (merge base ${mergeBase.slice(0, 12)})`);
for (const r of report) console.log(`  ${r}`);
if (!report.length) console.log('  no contract document changed');
if (problems.length) {
  console.log(`FAILED: ${problems.length} problem(s)`);
  for (const p of problems) console.log(`  ${p}`);
  process.exit(1);
}
console.log('OK: every changed contract document is versioned and in contracts/CHANGELOG.md');
