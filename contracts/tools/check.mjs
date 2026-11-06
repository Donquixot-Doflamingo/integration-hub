#!/usr/bin/env node
// contracts-check (HUB-015): everything `make contracts-check` verifies beyond Redocly lint.
//
//   node check.mjs [--root <contracts dir>] [--verbose]
//
// 1. Parse   every YAML/JSON file under contracts/ (tools/ excluded): YAML 1.2, duplicate keys,
//            anchors/aliases, and values that a YAML 1.1 parser would read differently
//            (unquoted timestamps, yes/no/on/off, octals).
// 2. $ref    every $ref in every file resolves (file + JSON pointer).
// 3. Schemas every JSON Schema under schemas/, every OpenAPI schema and every MCP tool schema is
//            meta-validated and compiled with ajv (draft 2020-12, strict: unknown keywords and
//            unknown formats fail).
// 4. Examples every example (media type example/examples, parameter and header examples, schema
//            `example`/`examples`, MCP tool examples, JSON Schema `examples`) validates against its
//            schema with 2020-12 semantics (`type: [x, 'null']`, `const`, `$ref` siblings).
//            Response examples must not carry `writeOnly` values; request examples must not carry
//            `readOnly` values. Prefixed ids (`req_`, `chg_`, ...) in examples must be ULIDs.
// 5. Rules   contracts/AGENTS.md: unique camelCase operationIds, declared tags, summary,
//            description, examples on every body, canonical error envelope on every 4xx/5xx,
//            admin `x-required-role`, `reason` + `change_id` on admin mutations, and (hub-admin
//            >= 1.1.0) `X-Hub-Request-Id` on every hub-admin response.
// 6. D12     no hub-public parameter, header or body field (and no caller-facing JSON Schema or
//            tenant MCP tool) names a vendor, account or version, and no caller example value
//            contains a vendor or account id used in hub-admin. Allow-listed: dryRunIntegration,
//            receiveVendorWebhook (README "Non-negotiables").
// 7. MCP     every `maps_to` operationId exists in its spec; tenant tools map to hub-public,
//            admin tools to hub-admin.
//    7b.     admin safety (HUB-314): maps_to http_method matches the spec; no tool maps to a DELETE
//            or a forbidden operation; every hub-admin DELETE is forbidden and every hub-admin
//            operation is classified once (tool / forbidden / not exposed); no secret (or, on
//            mutating tools, kill-switch) input property; destructiveHint=false, readOnlyHint
//            matches the mapped operation, x-required-role matches, mutating tools are stdio-only
//            with a required reason; requires_confirm / sensitive_output where required.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseDocument, parse as parseYaml, visit } from 'yaml';
import Ajv2020 from 'ajv/dist/2020.js';
import addFormats from 'ajv-formats';

// ---------------------------------------------------------------- configuration
const HERE = path.dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2);
const argVal = (name) => {
  const i = args.indexOf(name);
  return i >= 0 ? args[i + 1] : undefined;
};
const ROOT = path.resolve(argVal('--root') ?? path.join(HERE, '..'));
const VERBOSE = args.includes('--verbose');

const SPECS = {
  public: 'openapi/hub-public.v1.yaml',
  admin: 'openapi/hub-admin.v1.yaml',
  mock: 'openapi/mockvendor.v1.yaml',
};
const MCP = 'mcp/hubmcp-tools.json';
// Synthetic base URI so relative $refs between files resolve the same way everywhere
// (the checkout path may contain spaces).
const BASE_URI = 'https://contracts.invalid/';

// D12 allow-list (contracts/README.md "Non-negotiables enforced by the contract").
const LEAK_EXEMPT_OPS = new Set(['dryRunIntegration', 'receiveVendorWebhook']);
const LEAK_NAME = /vendor|account|version|transformer/i;
// Names that match LEAK_NAME but are documented as safe:
//   vendor_ref (opaque correlation id) and vendor_ms (a duration) - DESIGN §7.4, README;
//   spec_version - version of the callback envelope format, not of an integration;
//   X-Hub-Integration-Version - debug-only header, documented as "do not act on".
const LEAK_NAME_ALLOW = new Set(['vendor_ref', 'vendor_ms', 'spec_version', 'x-hub-integration-version']);

const HTTP_METHODS = ['get', 'put', 'post', 'delete', 'patch', 'head', 'options', 'trace'];
const ADMIN_ROLES = new Set(['admin', 'editor', 'viewer', 'tenant_viewer']);
// Admin POSTs that change nothing: no `reason`, no `change_id` (hub-admin info.description).
const ADMIN_NON_MUTATING = new Set([
  'testAccountConnection',
  'dryRunTransformer',
  'runTransformerGoldens',
  'validateIntegrationVersion',
]);
const ENVELOPE = {
  public: '#/components/schemas/ErrorEnvelope',
  admin: '#/components/schemas/AdminErrorEnvelope',
  mock: '#/components/schemas/MockErrorEnvelope',
};
const REQUEST_ID_HEADER = 'X-Hub-Request-Id';
const ULID_PREFIXES = ['req', 'chg', 'evt', 'dlv', 'ses', 'key', 'rec'];
const ID_LIKE = new RegExp(`\\b(?:${ULID_PREFIXES.join('|')})_[0-9A-Za-z]+`, 'g');
const ULID = new RegExp(`^(?:${ULID_PREFIXES.join('|')})_[0-9A-HJKMNP-TV-Z]{26}$`);
const SEMVER = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;

// ---------------------------------------------------------------- reporting
const problems = []; // { section, msg }
const warnings = [];
const stats = { files: 0, refs: 0, schemas: 0, examples: 0, operations: {} };
let section = 'parse';
const err = (msg) => problems.push({ section, msg });
const warn = (msg) => warnings.push({ section, msg });

// ---------------------------------------------------------------- helpers
const rel = (abs) => path.relative(ROOT, abs).split(path.sep).join('/');
const uriOf = (r) => BASE_URI + r;
const isObj = (v) => v !== null && typeof v === 'object' && !Array.isArray(v);
const escTok = (t) => String(t).replaceAll('~', '~0').replaceAll('/', '~1');
const ptrOf = (toks) => toks.map(escTok).join('/').replace(/^/, toks.length ? '/' : '');
// JSON pointer as used inside a URI fragment (ajv decodes it with decodeURIComponent).
const fragOf = (ptr) => ptr.split('/').map((t) => encodeURIComponent(t)).join('/');
const cmpSemver = (a, b) => {
  const pa = a.split('.').map(Number);
  const pb = b.split('.').map(Number);
  for (let i = 0; i < 3; i++) if (pa[i] !== pb[i]) return pa[i] - pb[i];
  return 0;
};

function walk(value, toks, fn) {
  fn(value, toks);
  if (Array.isArray(value)) value.forEach((v, i) => walk(v, [...toks, i], fn));
  else if (isObj(value)) for (const [k, v] of Object.entries(value)) walk(v, [...toks, k], fn);
}

function listFiles(dir) {
  const out = [];
  for (const ent of fs.readdirSync(dir, { withFileTypes: true })) {
    const abs = path.join(dir, ent.name);
    if (ent.isDirectory()) {
      if (abs === path.join(ROOT, 'tools') || ent.name === 'node_modules') continue;
      out.push(...listFiles(abs));
    } else if (/\.(ya?ml|json)$/.test(ent.name)) out.push(abs);
  }
  return out.sort();
}

// ---------------------------------------------------------------- 1. parse
const docs = new Map(); // rel -> parsed data
const idToRel = new Map(); // $id -> rel

// Report every place where a YAML 1.1 parser (PyYAML, go-yaml v2, ...) would read a value
// differently from the YAML 1.2 core schema used here.
function compare11(a, b, toks, file) {
  if (b instanceof Date) {
    err(`${file}#${ptrOf(toks)}: unquoted timestamp '${a}'; quote it (a YAML 1.1 parser reads a date)`);
    return;
  }
  if (typeof a !== typeof b || Array.isArray(a) !== Array.isArray(b)) {
    err(`${file}#${ptrOf(toks)}: ${JSON.stringify(a)} reads as ${JSON.stringify(b)} in YAML 1.1; quote it`);
    return;
  }
  if (Array.isArray(a)) a.forEach((v, i) => compare11(v, b[i], [...toks, i], file));
  else if (isObj(a)) {
    const kb = Object.keys(b);
    Object.keys(a).forEach((k, i) => {
      if (kb[i] !== k) err(`${file}#${ptrOf(toks)}: key '${k}' reads as '${kb[i]}' in YAML 1.1; quote it`);
      compare11(a[k], b[kb[i]], [...toks, k], file);
    });
  } else if (a !== b && !(Number.isNaN(a) && Number.isNaN(b))) {
    err(`${file}#${ptrOf(toks)}: ${JSON.stringify(a)} reads as ${JSON.stringify(b)} in YAML 1.1; quote it`);
  }
}

for (const abs of listFiles(ROOT)) {
  const r = rel(abs);
  const text = fs.readFileSync(abs, 'utf8');
  const isJson = r.endsWith('.json');
  const doc = parseDocument(text, { uniqueKeys: true, prettyErrors: true });
  if (doc.errors.length) {
    for (const e of doc.errors) err(`${r}: ${e.message.split('\n')[0]}`);
    continue;
  }
  let data;
  if (isJson) {
    try {
      data = JSON.parse(text);
    } catch (e) {
      err(`${r}: invalid JSON: ${e.message}`);
      continue;
    }
  } else {
    visit(doc, {
      Alias: (_, node) => err(`${r}: alias '*${node.source}' (no anchors or aliases, contracts/AGENTS.md)`),
      Node: (_, node) => {
        if (node.anchor) err(`${r}: anchor '&${node.anchor}' (no anchors or aliases, contracts/AGENTS.md)`);
      },
    });
    data = doc.toJS();
    compare11(data, parseYaml(text, { schema: 'yaml-1.1', uniqueKeys: false }), [], r);
  }
  docs.set(r, data);
  stats.files++;
  walk(data, [], (v) => {
    if (isObj(v) && typeof v.$id === 'string' && !v.$id.startsWith('#')) idToRel.set(v.$id, r);
  });
}

for (const [name, r] of Object.entries(SPECS)) if (!docs.has(r)) err(`missing or unparsable ${r} (${name})`);
if (!docs.has(MCP)) err(`missing or unparsable ${MCP}`);

// ---------------------------------------------------------------- 2. $ref resolution
section = '$ref';
function pointerGet(data, frag) {
  if (frag === '' || frag === '/') return data;
  if (!frag.startsWith('/')) throw new Error(`unsupported fragment '#${frag}'`);
  let cur = data;
  for (const raw of frag.slice(1).split('/')) {
    const tok = decodeURIComponent(raw).replaceAll('~1', '/').replaceAll('~0', '~');
    if (Array.isArray(cur) && /^\d+$/.test(tok) && Number(tok) < cur.length) cur = cur[Number(tok)];
    else if (isObj(cur) && Object.hasOwn(cur, tok)) cur = cur[tok];
    else throw new Error(`no '${tok}'`);
  }
  return cur;
}
// Returns { value, file, frag }.
function resolveRef(ref, fromRel) {
  const hash = ref.indexOf('#');
  const filePart = hash >= 0 ? ref.slice(0, hash) : ref;
  const frag = hash >= 0 ? ref.slice(hash + 1) : '';
  let file = fromRel;
  if (filePart) {
    if (/^[a-z][a-z0-9+.-]*:/i.test(filePart)) {
      file = idToRel.get(filePart);
      if (!file) throw new Error(`unknown $id ${filePart}`);
    } else {
      file = path.posix.normalize(path.posix.join(path.posix.dirname(fromRel), filePart));
    }
  }
  if (!docs.has(file)) throw new Error(`file ${file} not found`);
  return { value: pointerGet(docs.get(file), frag), file, frag };
}
// Follows a $ref chain on a non-schema object (parameter, response, example, ...).
function deref(obj, file) {
  let guard = 0;
  while (isObj(obj) && typeof obj.$ref === 'string' && guard++ < 20) {
    try {
      const t = resolveRef(obj.$ref, file);
      obj = t.value;
      file = t.file;
    } catch {
      return { value: undefined, file };
    }
  }
  return { value: obj, file };
}

for (const [r, data] of docs) {
  walk(data, [], (v, toks) => {
    if (!isObj(v) || !Object.hasOwn(v, '$ref')) return;
    // A property literally named "$ref" inside `properties` is not a reference.
    if (toks.at(-1) === 'properties') return;
    stats.refs++;
    if (typeof v.$ref !== 'string') return err(`${r}#${ptrOf(toks)}: $ref is not a string`);
    try {
      resolveRef(v.$ref, r);
    } catch (e) {
      err(`${r}#${ptrOf(toks)}: broken $ref '${v.$ref}' (${e.message})`);
    }
  });
}

// ---------------------------------------------------------------- 3. ajv
section = 'schema';
const xKeywords = new Set();
for (const data of docs.values())
  walk(data, [], (v) => {
    if (isObj(v)) for (const k of Object.keys(v)) if (k.startsWith('x-')) xKeywords.add(k);
  });

// mode: 'any' | 'request' | 'response'
function makeAjv(mode) {
  const ajv = new Ajv2020({
    strict: true, // unknown keywords and unknown formats are errors
    strictTypes: false, // OpenAPI allOf/oneOf fragments often omit `type`
    strictTuples: false,
    strictRequired: false, // `required` may name a property defined in an allOf sibling
    allowUnionTypes: true, // type: [string, 'null']
    allErrors: true,
    validateFormats: true,
    inlineRefs: false, // inlining a cross-file $ref would resolve its relative $refs against the wrong base
  });
  addFormats(ajv);
  // OpenAPI 3.1 Schema Object annotations (no validation meaning).
  for (const k of ['example', 'discriminator', 'xml', 'externalDocs']) ajv.addKeyword({ keyword: k });
  // Capability-level extensions documented in schemas/capabilities/README.md.
  for (const k of ['interactive', 'operations']) ajv.addKeyword({ keyword: k });
  for (const k of xKeywords) ajv.addKeyword({ keyword: k });
  const forbid = mode === 'response' ? 'writeOnly' : mode === 'request' ? 'readOnly' : null;
  if (forbid) {
    ajv.removeKeyword(forbid);
    ajv.addKeyword({
      keyword: forbid,
      schemaType: 'boolean',
      error: { message: `must not be present (${forbid}) in a ${mode} example` },
      validate: (sch) => !sch,
    });
  }
  for (const [r, data] of docs) {
    // Register each file as a resource so `$ref`s between files resolve. OpenAPI and MCP files
    // are not schemas: ajv compiles a resource root before resolving a pointer into it, and in
    // strict mode `openapi`, `paths`, ... are unknown keywords. Registering them with
    // non-enumerable keys gives ajv an empty root to compile while JSON-pointer lookups still
    // reach every subschema. Their subschemas are meta-validated and compiled one by one below.
    // A JSON Schema file without `$id` gets its file URI as `$id`: ajv otherwise resolves the
    // relative `$ref`s inside it against the base of whichever document referenced it.
    const resource = r.startsWith('schemas/')
      ? { $id: uriOf(r), ...data }
      : Object.defineProperties(
          {},
          Object.fromEntries(Object.entries(data).map(([k, v]) => [k, { value: v, enumerable: false }])),
        );
    try {
      ajv.addSchema(resource, uriOf(r), undefined, false);
    } catch (e) {
      if (mode === 'any') err(`${r}: cannot register with ajv: ${e.message}`);
    }
  }
  return ajv;
}
const AJV = { any: makeAjv('any'), request: makeAjv('request'), response: makeAjv('response') };
const compiled = new Map();
const compileErrors = new Map(); // message -> { where, count }
function validatorFor(file, ptr, mode = 'any') {
  const key = `${mode} ${file}#${ptr}`;
  if (compiled.has(key)) return compiled.get(key);
  let v;
  try {
    v = AJV[mode].compile({ $ref: `${uriOf(file)}#${fragOf(ptr)}` });
  } catch (e) {
    v = { compileError: e.message };
  }
  compiled.set(key, v);
  return v;
}
function metaCheck(schema, where) {
  stats.schemas++;
  if (typeof schema === 'boolean') return;
  if (!AJV.any.validateSchema(schema)) err(`${where}: not a valid 2020-12 schema: ${AJV.any.errorsText(AJV.any.errors)}`);
}
function compileCheck(file, ptr, where) {
  const v = validatorFor(file, ptr);
  if (!v.compileError) return;
  // One broken schema usually breaks every schema that references it: report each distinct
  // error once, with the first place it was seen and how many schemas it affects.
  const seen = compileErrors.get(v.compileError);
  if (seen) seen.count++;
  else compileErrors.set(v.compileError, { where, count: 1 });
}

// ---------------------------------------------------------------- 4. examples
const idValues = { vendors: new Set(), accounts: new Set() };
const callerExamples = []; // [where, value] scanned for vendor/account ids (D12)

function checkIds(value, where) {
  walk(value, [], (v, toks) => {
    if (typeof v !== 'string') return;
    for (const id of v.match(ID_LIKE) ?? [])
      if (!ULID.test(id)) err(`${where}${ptrOf(toks)}: '${id}' is not a well-formed ${id.split('_')[0]}_ + ULID`);
  });
}
function fmtErrors(errors) {
  return (errors ?? [])
    .slice(0, 5)
    .map((e) => `${e.instancePath || '/'} ${e.message}${e.params?.additionalProperty ? ` '${e.params.additionalProperty}'` : ''}`)
    .join('; ');
}
// Validates one example value against the schema at file#ptr.
function checkExample(value, file, ptr, where, mode = 'any', caller = false) {
  section = 'examples';
  stats.examples++;
  checkIds(value, where);
  if (caller) callerExamples.push([where, value]);
  const v = validatorFor(file, ptr, mode);
  if (v.compileError) return; // reported once by compileCheck
  if (!v(value)) err(`${where}: example does not match its schema: ${fmtErrors(v.errors)}`);
}

const SUBSCHEMA_MAPS = ['properties', 'patternProperties', '$defs', 'definitions', 'dependentSchemas'];
const SUBSCHEMA_ONE = ['items', 'additionalProperties', 'not', 'if', 'then', 'else', 'contains', 'propertyNames', 'unevaluatedItems', 'unevaluatedProperties'];
const SUBSCHEMA_LIST = ['allOf', 'anyOf', 'oneOf', 'prefixItems'];
// Visits a schema and its subschemas; validates schema-level `example` / `examples`.
function walkSchema(schema, file, toks, where, caller, seen = new Set()) {
  if (!isObj(schema)) return;
  const ptr = ptrOf(toks);
  if (seen.has(ptr)) return;
  seen.add(ptr);
  if (Object.hasOwn(schema, 'example')) checkExample(schema.example, file, ptr, `${where} example`, 'any', caller);
  if (Array.isArray(schema.examples))
    schema.examples.forEach((ex, i) => checkExample(ex, file, ptr, `${where} examples[${i}]`, 'any', caller));
  for (const k of SUBSCHEMA_MAPS)
    if (isObj(schema[k])) for (const [n, s] of Object.entries(schema[k])) walkSchema(s, file, [...toks, k, n], `${where}/${k}/${n}`, caller, seen);
  for (const k of SUBSCHEMA_ONE) if (isObj(schema[k])) walkSchema(schema[k], file, [...toks, k], `${where}/${k}`, caller, seen);
  for (const k of SUBSCHEMA_LIST)
    if (Array.isArray(schema[k])) schema[k].forEach((s, i) => walkSchema(s, file, [...toks, k, i], `${where}/${k}/${i}`, caller, seen));
}
// A schema that sits at file#toks (component, inline, standalone): meta-validate, compile, examples.
function checkSchemaAt(schema, file, toks, where, caller) {
  section = 'schema';
  metaCheck(schema, where);
  compileCheck(file, ptrOf(toks), where);
  walkSchema(schema, file, toks, where, caller);
}

// Media type, parameter or header object: example / examples (map, possibly $ref'd) vs its schema.
function objectExamples(obj, file, toks) {
  const out = [];
  if (Object.hasOwn(obj, 'example')) out.push(['example', obj.example]);
  if (isObj(obj.examples))
    for (const [name, ex] of Object.entries(obj.examples)) {
      const { value } = deref(ex, file);
      if (isObj(value) && Object.hasOwn(value, 'value')) out.push([`examples.${name}`, value.value]);
      else if (isObj(value) && value.externalValue) warn(`${file}${ptrOf([...toks, 'examples', name])}: externalValue is not validated`);
    }
  return out;
}

// ---------------------------------------------------------------- 5. OpenAPI rules
const opsBySpec = {}; // kind -> [{ sec, p, m, op, toks }]

// Collects { props, required } through $ref and allOf.
function schemaProps(schema, file, depth = 0) {
  const out = { props: {}, required: new Set() };
  if (depth > 15) return out;
  const { value: s, file: f } = deref(schema, file);
  if (!isObj(s)) return out;
  Object.assign(out.props, s.properties ?? {});
  for (const r of s.required ?? []) out.required.add(r);
  for (const sub of s.allOf ?? []) {
    const o = schemaProps(sub, f, depth + 1);
    Object.assign(out.props, o.props);
    for (const r of o.required) out.required.add(r);
  }
  return out;
}

function checkMedia(content, file, toks, where, mode, caller, needExample) {
  for (const [ct, mt] of Object.entries(content ?? {})) {
    const mtoks = [...toks, 'content', ct];
    const exs = objectExamples(mt, file, mtoks);
    if (needExample && exs.length === 0) {
      section = 'rules';
      err(`${where} ${ct}: no example (every request and response body needs one)`);
    }
    if (!isObj(mt.schema)) continue;
    checkSchemaAt(mt.schema, file, [...mtoks, 'schema'], `${where} ${ct} schema`, caller);
    for (const [name, ex] of exs) checkExample(ex, file, ptrOf([...mtoks, 'schema']), `${where} ${ct} ${name}`, mode, caller);
  }
}
function checkParamOrHeader(obj, file, toks, where, caller, mode) {
  if (isObj(obj.schema)) {
    checkSchemaAt(obj.schema, file, [...toks, 'schema'], `${where} schema`, caller);
    for (const [name, ex] of objectExamples(obj, file, toks)) checkExample(ex, file, ptrOf([...toks, 'schema']), `${where} ${name}`, mode, caller);
  }
  if (isObj(obj.content)) checkMedia(obj.content, file, toks, where, mode, caller, false);
}
function checkHeaders(headers, file, toks, where, caller) {
  for (const [hn, h] of Object.entries(headers ?? {})) {
    if (isObj(h) && h.$ref) continue; // checked at its component
    checkParamOrHeader(h, file, [...toks, 'headers', hn], `${where} header ${hn}`, caller, 'response');
  }
}
function checkResponseObj(r, file, toks, where, caller, needExample) {
  checkHeaders(r.headers, file, toks, where, caller);
  checkMedia(r.content, file, toks, where, 'response', caller, needExample);
}
function hasRequestIdHeader(r, file) {
  const h = r.headers?.[REQUEST_ID_HEADER];
  if (!h) return false;
  const { value } = deref(h, file);
  return isObj(value) && value.required === true;
}

function checkSpec(kind) {
  const file = SPECS[kind];
  const doc = docs.get(file);
  if (!doc) return;
  section = 'rules';
  const version = doc.info?.version;
  if (doc.openapi !== '3.1.0') err(`${file}: openapi must be 3.1.0`);
  if (doc.jsonSchemaDialect !== 'https://json-schema.org/draft/2020-12/schema') err(`${file}: jsonSchemaDialect must be 2020-12`);
  if (!SEMVER.test(String(version))) err(`${file}: info.version '${version}' is not semver`);
  const requireRequestId = kind === 'admin' && SEMVER.test(String(version)) && cmpSemver(version, '1.1.0') >= 0;
  const tags = new Set((doc.tags ?? []).map((t) => t.name));
  const ops = [];
  const oids = new Map();

  for (const sec of ['paths', 'webhooks']) {
    for (const [p, rawItem] of Object.entries(doc[sec] ?? {})) {
      const { value: item } = deref(rawItem, file);
      if (!isObj(item)) continue;
      const itemToks = [sec, p];
      for (const m of HTTP_METHODS) {
        const op = item[m];
        if (!isObj(op)) continue;
        const toks = [...itemToks, m];
        ops.push({ sec, p, m, op, toks });
        const oid = op.operationId;
        const where = `${file} ${m.toUpperCase()} ${p}${oid ? ` (${oid})` : ''}`;
        const caller = kind === 'public' && !LEAK_EXEMPT_OPS.has(oid);
        section = 'rules';
        if (!oid) err(`${where}: no operationId`);
        else {
          if (!/^[a-z][a-zA-Z0-9]*$/.test(oid)) err(`${where}: operationId is not camelCase`);
          if (oids.has(oid)) err(`${where}: duplicate operationId (also ${oids.get(oid)})`);
          oids.set(oid, `${m.toUpperCase()} ${p}`);
        }
        for (const k of ['summary', 'description']) if (!String(op[k] ?? '').trim()) err(`${where}: missing ${k}`);
        if (!op.tags?.length) err(`${where}: no tags`);
        for (const t of op.tags ?? []) if (!tags.has(t)) err(`${where}: tag '${t}' is not declared`);
        if (kind === 'admin' && !ADMIN_ROLES.has(op['x-required-role'])) err(`${where}: x-required-role '${op['x-required-role']}' is not one of ${[...ADMIN_ROLES]}`);

        // parameters
        const params = [];
        for (const [list, ltoks] of [
          [item.parameters, [...itemToks, 'parameters']],
          [op.parameters, [...toks, 'parameters']],
        ]) {
          (list ?? []).forEach((raw, i) => {
            const { value: pp, file: pf } = deref(raw, file);
            if (!isObj(pp)) return;
            params.push(pp);
            if (!raw.$ref) checkParamOrHeader(pp, pf, [...ltoks, i], `${where} param ${pp.name}`, caller, 'request');
          });
        }
        section = 'rules';
        for (const pp of params) if (pp.in === 'path' && pp.required !== true) err(`${where}: path param '${pp.name}' must be required`);
        for (const [, nm] of p.matchAll(/\{([^}]+)\}/g))
          if (sec === 'paths' && !params.some((pp) => pp.in === 'path' && pp.name === nm)) err(`${where}: path template {${nm}} has no parameter`);

        // request body
        const mutating = kind === 'admin' && ['post', 'put', 'patch', 'delete'].includes(m) && !ADMIN_NON_MUTATING.has(oid);
        if (op.requestBody) {
          const { value: rb, file: rbf } = deref(op.requestBody, file);
          if (isObj(rb)) {
            const rbToks = op.requestBody.$ref ? null : [...toks, 'requestBody'];
            if (rbToks) checkMedia(rb.content, rbf, rbToks, `${where} request`, 'request', caller, true);
            if (mutating && m !== 'delete') {
              const { props, required } = schemaProps(rb.content?.['application/json']?.schema, rbf);
              section = 'rules';
              if (!props.reason || !required.has('reason')) err(`${where}: admin mutation without a required 'reason' in the body`);
            }
          }
        } else if (mutating && m !== 'delete') err(`${where}: admin mutation without a body carrying 'reason'`);
        if (mutating && m === 'delete' && !params.some((pp) => pp.name === 'reason' && pp.in === 'query' && pp.required === true))
          err(`${where}: admin DELETE without a required ?reason=`);

        // responses
        section = 'rules';
        const responses = op.responses ?? {};
        if (!Object.keys(responses).length) err(`${where}: no responses`);
        let hasChangeId = false;
        for (const [code, raw] of Object.entries(responses)) {
          const { value: r, file: rf } = deref(raw, file);
          const rw = `${where} -> ${code}`;
          section = 'rules';
          if (!isObj(r)) continue;
          if (!String(r.description ?? '').trim()) err(`${rw}: no description`);
          const content = r.content ?? {};
          if (!Object.keys(content).length && !['204', '302', '304'].includes(String(code))) err(`${rw}: response without content`);
          if (/^[45]/.test(String(code)))
            for (const [ct, mt] of Object.entries(content))
              if (mt.schema?.$ref !== ENVELOPE[kind]) err(`${rw} ${ct}: error response does not use ${ENVELOPE[kind]}`);
          if (/^2/.test(String(code)))
            for (const mt of Object.values(content)) if (schemaProps(mt.schema, rf).props.change_id) hasChangeId = true;
          if (requireRequestId && sec === 'paths' && !hasRequestIdHeader(r, rf)) err(`${rw}: does not declare the required ${REQUEST_ID_HEADER} header (hub-admin >= 1.1.0)`);
          if (!raw.$ref) checkResponseObj(r, rf, [...toks, 'responses', code], rw, caller, true);
        }
        section = 'rules';
        if (mutating && !hasChangeId) err(`${where}: admin mutation whose 2xx response has no change_id`);
      }
    }
  }

  // components (validated once here, not at every $ref)
  const c = doc.components ?? {};
  const callerComp = kind === 'public';
  for (const [n, s] of Object.entries(c.schemas ?? {})) checkSchemaAt(s, file, ['components', 'schemas', n], `${file} #/components/schemas/${n}`, callerComp);
  for (const [n, pp] of Object.entries(c.parameters ?? {}))
    if (!pp.$ref) checkParamOrHeader(pp, file, ['components', 'parameters', n], `${file} #/components/parameters/${n}`, callerComp, 'request');
  for (const [n, h] of Object.entries(c.headers ?? {}))
    if (!h.$ref) checkParamOrHeader(h, file, ['components', 'headers', n], `${file} #/components/headers/${n}`, callerComp, 'response');
  for (const [n, r] of Object.entries(c.responses ?? {}))
    if (!r.$ref) {
      section = 'rules';
      if (requireRequestId && !hasRequestIdHeader(r, file)) err(`${file} #/components/responses/${n}: does not declare the required ${REQUEST_ID_HEADER} header (hub-admin >= 1.1.0)`);
      checkResponseObj(r, file, ['components', 'responses', n], `${file} #/components/responses/${n}`, callerComp, true);
    }
  for (const [n, rb] of Object.entries(c.requestBodies ?? {}))
    if (!rb.$ref) checkMedia(rb.content, file, ['components', 'requestBodies', n], `${file} #/components/requestBodies/${n}`, 'request', callerComp, true);

  stats.operations[file] = ops.length;
  opsBySpec[kind] = ops;
}

// ---------------------------------------------------------------- run 3-5 on the OpenAPI documents
for (const kind of Object.keys(SPECS)) checkSpec(kind);

// standalone JSON Schemas
for (const [r, data] of docs) {
  if (!r.startsWith('schemas/')) continue;
  section = 'schema';
  if (data.$schema !== 'https://json-schema.org/draft/2020-12/schema') err(`${r}: $schema must be 2020-12`);
  checkSchemaAt(data, r, [], r, true);
}

section = 'schema';
for (const [msg, { where, count }] of compileErrors)
  err(`ajv cannot compile: ${msg} (first at ${where}${count > 1 ? `; ${count} schemas affected` : ''})`);

// ---------------------------------------------------------------- 6. D12 vendor-leak check (hub-public)
section = 'leak (D12)';
const leakName = (name) => LEAK_NAME.test(name) && !LEAK_NAME_ALLOW.has(String(name).toLowerCase());
function scanSchemaNames(schema, file, where, seen = new Set(), depth = 0) {
  if (depth > 40) return;
  const { value: s, file: f } = deref(schema, file);
  if (!isObj(s) || seen.has(s)) return;
  seen.add(s);
  for (const [k, v] of Object.entries(s.properties ?? {})) {
    if (leakName(k)) err(`${where}: body field '${k}' names a vendor, account or version`);
    scanSchemaNames(v, f, `${where}/${k}`, seen, depth + 1);
  }
  for (const k of ['items', 'additionalProperties', 'not', 'if', 'then', 'else', 'contains', 'unevaluatedProperties'])
    if (isObj(s[k])) scanSchemaNames(s[k], f, where, seen, depth + 1);
  for (const k of ['allOf', 'anyOf', 'oneOf', 'prefixItems']) for (const sub of s[k] ?? []) scanSchemaNames(sub, f, where, seen, depth + 1);
  // A schema's own $ref with siblings: deref above only followed a pure chain.
  if (typeof schema?.$ref === 'string' && s !== schema) return;
}
const pub = docs.get(SPECS.public);
if (pub) {
  for (const { sec, p, m, op, toks } of opsBySpec.public ?? []) {
    if (LEAK_EXEMPT_OPS.has(op.operationId)) continue;
    const where = `hub-public ${m.toUpperCase()} ${p} (${op.operationId})`;
    const item = deref(pub[sec][p], SPECS.public).value;
    for (const raw of [...(item.parameters ?? []), ...(op.parameters ?? [])]) {
      const { value: pp } = deref(raw, SPECS.public);
      if (isObj(pp) && leakName(pp.name)) err(`${where}: ${pp.in} parameter '${pp.name}' names a vendor, account or version`);
      if (isObj(pp?.schema)) scanSchemaNames(pp.schema, SPECS.public, `${where} param ${pp.name}`);
    }
    if (op.requestBody) {
      const { value: rb, file: f } = deref(op.requestBody, SPECS.public);
      for (const mt of Object.values(rb?.content ?? {})) scanSchemaNames(mt.schema, f, `${where} request`);
    }
    for (const [code, raw] of Object.entries(op.responses ?? {})) {
      const { value: r, file: f } = deref(raw, SPECS.public);
      for (const hn of Object.keys(r?.headers ?? {})) if (leakName(hn)) err(`${where} -> ${code}: header '${hn}' names a vendor, account or version`);
      for (const mt of Object.values(r?.content ?? {})) scanSchemaNames(mt.schema, f, `${where} -> ${code}`);
    }
    void toks;
  }
}
// Caller-facing JSON Schemas (capabilities, callback events).
for (const r of docs.keys()) if (r.startsWith('schemas/')) scanSchemaNames(docs.get(r), r, r);

// Vendor and account ids known from hub-admin examples must not appear in caller examples.
const admin = docs.get(SPECS.admin);
if (admin)
  walk(admin, [], (v) => {
    if (!isObj(v)) return;
    for (const [k, set] of [
      ['vendor_id', idValues.vendors],
      ['account_id', idValues.accounts],
    ])
      if (typeof v[k] === 'string' && v[k].length >= 4) set.add(v[k]);
  });
// The schema of hub-admin also has `example:` on parameters; those are covered by the walk above
// only when the parameter is literally named vendor_id/account_id, so add parameter examples too.
for (const pp of Object.values(admin?.components?.parameters ?? {})) {
  if (pp?.name === 'vendor_id' && typeof pp.example === 'string') idValues.vendors.add(pp.example);
  if (pp?.name === 'account_id' && typeof pp.example === 'string') idValues.accounts.add(pp.example);
}
const knownIds = [...idValues.vendors, ...idValues.accounts];
function scanValues(value, where) {
  walk(value, [], (v, toks) => {
    if (typeof v !== 'string') return;
    const low = v.toLowerCase();
    for (const id of knownIds)
      if (new RegExp(`(^|[^A-Za-z0-9_-])${id.toLowerCase().replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}($|[^A-Za-z0-9_-])`).test(low))
        err(`${where}${ptrOf(toks)}: caller example contains vendor/account id '${id}'`);
  });
}
for (const [where, value] of callerExamples) scanValues(value, where);

// ---------------------------------------------------------------- 7. MCP tool contract
section = 'mcp';
const mcp = docs.get(MCP);
if (mcp) {
  if (!SEMVER.test(String(mcp.version))) err(`${MCP}: version '${mcp.version}' is not semver`);
  const opIds = {};
  for (const [kind, f] of Object.entries(SPECS)) opIds[path.posix.basename(f)] = new Set((opsBySpec[kind] ?? []).map((o) => o.op.operationId));
  const names = new Set();
  (mcp.tools ?? []).forEach((t, i) => {
    const where = `${MCP} tool ${t.name ?? i}`;
    section = 'mcp';
    if (names.has(t.name)) err(`${where}: duplicate tool name`);
    names.add(t.name);
    for (const k of ['name', 'description', 'inputSchema', 'outputSchema', 'maps_to', 'safety', 'audience'])
      if (!Object.hasOwn(t, k)) err(`${where}: missing '${k}'`);
    const isAdmin = t.audience === 'admin';
    if (!['tenant', 'admin'].includes(t.audience)) err(`${where}: audience must be tenant or admin`);
    if (isAdmin !== String(t.name).startsWith('hub_admin_')) err(`${where}: admin tools (and only they) are named hub_admin_*`);
    const maps = Array.isArray(t.maps_to) ? t.maps_to : [];
    if (!maps.length) err(`${where}: maps_to is empty`);
    for (const mt of maps) {
      if (!opIds[mt.spec]) err(`${where}: maps_to spec '${mt.spec}' is not a contract document`);
      else if (!opIds[mt.spec].has(mt.operationId)) err(`${where}: maps_to ${mt.spec}#${mt.operationId} does not exist`);
      if (isAdmin && mt.spec !== 'hub-admin.v1.yaml') err(`${where}: admin tool maps to ${mt.spec}`);
      if (!isAdmin && mt.spec !== 'hub-public.v1.yaml') err(`${where}: tenant tool maps to ${mt.spec}`);
    }
    const exempt = maps.length > 0 && maps.every((mt) => LEAK_EXEMPT_OPS.has(mt.operationId));
    const caller = !isAdmin && !exempt;
    for (const k of ['inputSchema', 'outputSchema']) {
      if (!isObj(t[k])) continue;
      checkSchemaAt(t[k], MCP, ['tools', i, k], `${where} ${k}`, caller);
      if (caller) {
        section = 'leak (D12)';
        scanSchemaNames(t[k], MCP, `${where} ${k}`);
      }
    }
    (t.examples ?? []).forEach((ex, j) => {
      if (Object.hasOwn(ex, 'input')) checkExample(ex.input, MCP, ptrOf(['tools', i, 'inputSchema']), `${where} examples[${j}].input`);
      if (Object.hasOwn(ex, 'output')) checkExample(ex.output, MCP, ptrOf(['tools', i, 'outputSchema']), `${where} examples[${j}].output`);
      if (caller) {
        section = 'leak (D12)';
        scanValues(ex, `${where} examples[${j}]`);
      }
    });
  });

  // ---- 7b. Admin safety rules (HUB-314, contracts/mcp/README.md "Admin write tools").
  section = 'mcp safety';
  const opInfo = {}; // spec basename -> operationId -> { method, role, mutating }
  for (const [kind, f] of Object.entries(SPECS)) {
    const m = (opInfo[path.posix.basename(f)] = {});
    for (const o of opsBySpec[kind] ?? [])
      m[o.op.operationId] = {
        method: o.m.toUpperCase(),
        role: o.op['x-required-role'],
        mutating: kind === 'admin' && ['post', 'put', 'patch', 'delete'].includes(o.m) && !ADMIN_NON_MUTATING.has(o.op.operationId),
      };
  }
  const ADMIN_SPEC = path.posix.basename(SPECS.admin);
  const ROLE_RANK = { tenant_viewer: 0, viewer: 1, editor: 2, admin: 3 };
  // Operations that must carry the confirm guard / a sensitive_output declaration whatever else
  // the contract says (the user's requirement; README "Admin write tools").
  const MUST_CONFIRM_LIVE = new Set(['publishIntegrationVersion', 'updateIntegrationWeights', 'rollbackIntegration']);
  const MUST_DECLARE_SECRET = new Set(['createApiKey', 'createRegistrarEndpoint']);
  const CONFIRM_KINDS = new Set(['live_traffic', 'mints_secret']);
  const strList = (v, name) => {
    if (!Array.isArray(v) || !v.every((x) => typeof x === 'string' && x)) {
      err(`${MCP}: ${name} must be an array of non-empty strings`);
      return new Set();
    }
    return new Set(v);
  };
  const forbiddenIn = strList(mcp.forbidden_input_properties, 'forbidden_input_properties');
  const forbiddenMutIn = strList(mcp.forbidden_mutation_input_properties, 'forbidden_mutation_input_properties');
  if (!Array.isArray(mcp.secret_patterns) || !mcp.secret_patterns.length) err(`${MCP}: secret_patterns must be a non-empty array`);
  for (const sp of mcp.secret_patterns ?? []) {
    try {
      new RegExp(sp.pattern);
    } catch (e) {
      err(`${MCP}: secret_patterns '${sp?.name}' is not a valid regex: ${e.message}`);
    }
  }
  // Every property name anywhere in a schema (properties, items, combinators), through $refs.
  function propNames(schema, file, out = new Set(), depth = 0) {
    if (depth > 40) return out;
    const { value: s, file: f } = deref(schema, file);
    if (!isObj(s)) return out;
    for (const [k, v] of Object.entries(s.properties ?? {})) {
      out.add(k);
      propNames(v, f, out, depth + 1);
    }
    for (const k of ['items', 'additionalProperties', 'not', 'if', 'then', 'else', 'contains'])
      if (isObj(s[k])) propNames(s[k], f, out, depth + 1);
    for (const k of ['allOf', 'anyOf', 'oneOf', 'prefixItems']) for (const sub of s[k] ?? []) propNames(sub, f, out, depth + 1);
    return out;
  }
  // Classification lists: each entry names an existing operation with its real method.
  const classified = new Map(); // operationId -> where it is classified
  const classify = (id, where) => {
    if (classified.has(id)) err(`${where}: ${ADMIN_SPEC}#${id} is also ${classified.get(id)}`);
    else classified.set(id, where);
  };
  for (const listName of ['forbidden_operations', 'not_exposed_operations']) {
    if (!Array.isArray(mcp[listName])) {
      err(`${MCP}: missing array ${listName}`);
      continue;
    }
    mcp[listName].forEach((e, i) => {
      const where = `${MCP} ${listName}[${i}] ${e?.operationId}`;
      if (e?.spec !== ADMIN_SPEC) return err(`${where}: spec must be ${ADMIN_SPEC}`);
      const info = opInfo[ADMIN_SPEC][e.operationId];
      if (!info) return err(`${where}: operation does not exist`);
      if (e.http_method !== info.method) err(`${where}: http_method '${e.http_method}' is not the spec's ${info.method}`);
      if (!String(e.reason ?? '').trim()) err(`${where}: missing reason`);
      classify(e.operationId, listName);
    });
  }
  const forbiddenIds = new Set((mcp.forbidden_operations ?? []).map((e) => e?.operationId));
  for (const spec of Object.keys(opInfo))
    for (const [id, info] of Object.entries(opInfo[spec]))
      if (spec === ADMIN_SPEC && info.method === 'DELETE' && !forbiddenIds.has(id)) err(`${MCP}: ${spec}#${id} is an HTTP DELETE but not in forbidden_operations`);

  (mcp.tools ?? []).forEach((t, i) => {
    const where = `${MCP} tool ${t.name ?? i}`;
    const isAdmin = t.audience === 'admin';
    const maps = Array.isArray(t.maps_to) ? t.maps_to : [];
    let mutating = false;
    let role = null;
    for (const mt of maps) {
      const info = opInfo[mt.spec]?.[mt.operationId];
      if (!info) continue; // reported above
      if (mt.http_method !== info.method) err(`${where}: maps_to ${mt.operationId} http_method '${mt.http_method}' is not the spec's ${info.method}`);
      if (info.method === 'DELETE') err(`${where}: maps to an HTTP DELETE (${mt.operationId})`);
      if (forbiddenIds.has(mt.operationId)) err(`${where}: maps to forbidden operation ${mt.operationId}`);
      if (isAdmin) {
        if (classified.has(mt.operationId) && classified.get(mt.operationId) !== 'a tool') err(`${where}: maps to ${mt.operationId}, which is listed in ${classified.get(mt.operationId)}`);
        classified.set(mt.operationId, 'a tool');
        mutating ||= info.mutating;
        if (role === null || ROLE_RANK[info.role] > ROLE_RANK[role]) role = info.role;
      }
    }
    if (!isAdmin) return;
    const ann = t.annotations ?? {};
    if (ann.destructiveHint !== false) err(`${where}: admin tools must set destructiveHint=false (destructive operations are excluded)`);
    if (ann.readOnlyHint !== !mutating) err(`${where}: readOnlyHint must be ${!mutating} (it maps to ${mutating ? 'a mutating' : 'only non-mutating'} operation${maps.length > 1 ? 's' : ''})`);
    if (t.requires?.env !== 'HUB_ADMIN_TOKEN') err(`${where}: requires.env must be HUB_ADMIN_TOKEN`);
    if (role && t.requires?.['x-required-role'] !== role) err(`${where}: requires.x-required-role '${t.requires?.['x-required-role']}' is not the spec's ${role}`);
    const transport = t.requires?.transport;
    if (mutating && !(Array.isArray(transport) && transport.length === 1 && transport[0] === 'stdio')) err(`${where}: a mutating admin tool must declare requires.transport ["stdio"]`);
    const input = isObj(t.inputSchema) ? t.inputSchema : {};
    const names = propNames(input, MCP);
    for (const n of names) {
      if (forbiddenIn.has(n)) err(`${where}: input property '${n}' is in forbidden_input_properties (no secrets through MCP)`);
      if (mutating && forbiddenMutIn.has(n)) err(`${where}: input property '${n}' is in forbidden_mutation_input_properties`);
    }
    const props = input.properties ?? {};
    const required = new Set(input.required ?? []);
    if (mutating) {
      if (!props.reason || !required.has('reason')) err(`${where}: a mutating admin tool must require 'reason'`);
      else if (props.reason.type !== 'string' || !(props.reason.minLength >= 3) || !(props.reason.maxLength <= 1000))
        err(`${where}: reason must be a string of 3..1000 characters (room for the " (via hubmcp)" marker)`);
    }
    const ids = maps.map((mt) => mt.operationId);
    if (Object.hasOwn(t, 'requires_confirm')) {
      if (!CONFIRM_KINDS.has(t.requires_confirm)) err(`${where}: requires_confirm must be one of ${[...CONFIRM_KINDS]}`);
      if (!mutating) err(`${where}: requires_confirm on a non-mutating tool`);
      if (props.confirm?.type !== 'boolean' || props.confirm?.default !== false) err(`${where}: requires_confirm needs input 'confirm' (boolean, default false)`);
      if (!/confirm=true/.test(String(t.description))) err(`${where}: requires_confirm, but the description does not tell the model to pass confirm=true`);
    } else if (props.confirm && isAdmin) err(`${where}: has a 'confirm' input but no requires_confirm`);
    if (ids.some((id) => MUST_CONFIRM_LIVE.has(id)) && t.requires_confirm !== 'live_traffic') err(`${where}: changes live traffic and must declare requires_confirm "live_traffic"`);
    if (ids.some((id) => MUST_DECLARE_SECRET.has(id))) {
      if (t.requires_confirm !== 'mints_secret') err(`${where}: mints a secret and must declare requires_confirm "mints_secret"`);
      if (!Array.isArray(t.sensitive_output) || !t.sensitive_output.length) err(`${where}: returns a write-once secret and must declare sensitive_output`);
    }
    for (const ptr of t.sensitive_output ?? []) {
      const key = typeof ptr === 'string' && /^\/[^/]+$/.test(ptr) ? ptr.slice(1) : null;
      if (!key || !t.outputSchema?.properties?.[key]) err(`${where}: sensitive_output '${ptr}' is not a top-level property of outputSchema`);
      else if (!(t.outputSchema.required ?? []).includes(key)) err(`${where}: sensitive_output '${ptr}' must be required in outputSchema`);
      if (!/SENSITIVE/.test(String(t.description))) err(`${where}: sensitive_output, but the description has no SENSITIVE OUTPUT instruction`);
    }
  });
  for (const id of Object.keys(opInfo[ADMIN_SPEC]))
    if (!classified.has(id)) err(`${MCP}: ${ADMIN_SPEC}#${id} is not classified: map it from a tool or list it in forbidden_operations or not_exposed_operations`);
}

// ---------------------------------------------------------------- report
const versions = Object.values(SPECS)
  .map((f) => `${path.posix.basename(f)} ${docs.get(f)?.info?.version ?? '?'}`)
  .concat(mcp ? [`hubmcp-tools.json ${mcp.version}`] : []);
console.log(`contracts-check: ${ROOT}`);
console.log(`  versions:  ${versions.join(', ')}`);
console.log(`  parsed ${stats.files} files, resolved ${stats.refs} $refs, compiled ${stats.schemas} schemas, validated ${stats.examples} examples`);
console.log(`  operations: ${Object.entries(stats.operations).map(([f, n]) => `${path.posix.basename(f)} ${n}`).join(', ')}`);
console.log(`  D12 known vendor/account ids scanned in caller examples: ${knownIds.length}`);
if (warnings.length) {
  console.log(`warnings (${warnings.length}):`);
  for (const w of VERBOSE ? warnings : warnings.slice(0, 20)) console.log(`  [${w.section}] ${w.msg}`);
}
if (problems.length) {
  console.log(`FAILED: ${problems.length} problem(s)`);
  for (const p of problems) console.log(`  [${p.section}] ${p.msg}`);
  process.exit(1);
}
console.log('OK: all contract checks passed');
