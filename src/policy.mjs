import { lstat, readFile } from 'node:fs/promises';
import path from 'node:path';
import { gunzipSync } from 'node:zlib';
import { Parser } from 'tar';

export const MAX_FILE_BYTES = 8 * 1024 * 1024;
export const MAX_PACK_BYTES = 32 * 1024 * 1024;
export const RECEIPT = '.radpack/install.json';

export function safePath(value) {
  if (typeof value !== 'string' || !value || value.length > 240 ||
      /[^\x20-\x7e]|[\\:*?"<>|]/.test(value) || path.posix.isAbsolute(value)) {
    throw new Error('Pack paths must be portable, relative file paths.');
  }
  const parts = value.split('/');
  if (parts.some(part => !part || part === '.' || part === '..' ||
      /[. ]$/.test(part) || /^(con|prn|aux|nul|com[0-9]|lpt[0-9])(?:\.|$)/i.test(part) ||
      /^(?:\.git|\.?git~[0-9]+)$/i.test(part))) {
    throw new Error(`Unsafe pack path: ${JSON.stringify(value)}.`);
  }
  return value;
}

export function excludedReason(file) {
  safePath(file);
  const parts = file.toLowerCase().split('/');
  const name = parts.at(-1);
  if (parts.some(part => ['.rad', '.radpack', '.kube', '.aws', '.azure', '.ssh', '.docker',
    '.terraform', 'node_modules', '.staging', 'logs'].includes(part) || part.startsWith('.staging-'))) {
    return 'local configuration, state, cache, or reserved Radpack metadata';
  }
  if (/^\.env(?:\.|$)/.test(name) && !/^(?:\.env\.example|\.env\.sample|\.env\.template)$/.test(name)) {
    return 'environment values';
  }
  if (/credential|kubeconfig|\.tfstate(?:\.|$)|\.tfvars(?:\.|$)|\.bicepparam$|\.parameters\.json$|\.pem$|\.key$|^id_(rsa|ed25519|ecdsa)$|\.log$/.test(name)) {
    return 'credentials, deployment parameters, or state';
  }
  if (['.npmrc', '.netrc', '.pypirc', 'secrets.json', 'secrets.yaml', 'secrets.yml'].includes(name)) {
    return 'credential-bearing configuration';
  }
  if (['radius-environment.bicep', 'radius-recipe-pack.bicep', 'app-graph.json',
    'radius-state', 'radius-state.tar.gz'].includes(name)) {
    return 'generated environment, provider pack, or graph/state output';
  }
  if (file.toLowerCase().startsWith('.github/workflows/')) {
    return 'workflows are supplied by the consumer, not installed by a pack';
  }
  return null;
}

export async function readRegular(root, file) {
  safePath(file);
  let current = root;
  const parts = file.split('/');
  for (let index = 0; index < parts.length; index++) {
    current = path.join(current, parts[index]);
    let stat;
    try {
      stat = await lstat(current);
    } catch (error) {
      if (error.code === 'ENOENT') return null;
      throw error;
    }
    if (stat.isSymbolicLink()) throw new Error(`Symbolic links are not allowed: ${file}.`);
    if (index < parts.length - 1) {
      if (!stat.isDirectory()) throw new Error(`Not a directory in path: ${file}.`);
    } else {
      if (!stat.isFile()) throw new Error(`Not a regular file: ${file}.`);
      if (stat.size > MAX_FILE_BYTES) throw new Error(`File exceeds the 8 MiB limit: ${file}.`);
      const bytes = await readFile(current);
      if (bytes.length > MAX_FILE_BYTES) throw new Error(`File exceeds the 8 MiB limit: ${file}.`);
      return { bytes, mode: stat.mode & 0o111 ? '100755' : '100644' };
    }
  }
  return null;
}

export function textOf(bytes, file) {
  if (bytes.includes(0)) throw new Error(`Binary payloads are not supported in format v1: ${file}.`);
  try {
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  } catch {
    throw new Error(`Only UTF-8 text payloads are supported in format v1: ${file}.`);
  }
}

function resourceBody(text, start) {
  let depth = 0;
  let quote = null;
  let comment = null;
  for (let index = start; index < text.length; index++) {
    const char = text[index];
    const next = text[index + 1];
    if (comment === 'line') {
      if (char === '\n') comment = null;
      continue;
    }
    if (comment === 'block') {
      if (char === '*' && next === '/') { comment = null; index++; }
      continue;
    }
    if (quote) {
      if (char === '\\') { index++; continue; }
      if (char === quote) {
        if (next === quote) { index++; continue; }
        quote = null;
      }
      continue;
    }
    if (char === '/' && next === '/') { comment = 'line'; index++; continue; }
    if (char === '/' && next === '*') { comment = 'block'; index++; continue; }
    if (char === "'" || char === '"') { quote = char; continue; }
    if (char === '{') depth++;
    if (char === '}' && --depth === 0) return text.slice(start, index + 1);
  }
  throw new Error('Cannot inspect an unterminated Radius Secret resource.');
}

export function inspectContent(bytes, file) {
  const text = textOf(bytes, file);
  const findings = [];
  const rules = [
    ['private key', /-----BEGIN (?:[A-Z0-9 ]+ )?PRIVATE KEY-----/],
    ['GitHub token', /\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})\b/],
    ['npm token', /\bnpm_[A-Za-z0-9]{20,}\b/],
    ['AWS access key', /\b(?:AKIA|ASIA)[A-Z0-9]{16}\b/],
    ['credential-bearing URL', /\b[a-z][a-z0-9+.-]*:\/\/[^/\s:@"']+:[^/\s@"']+@/i],
    ['JWT-like token', /\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b/],
    ['cloud resource binding', /(?:\/subscriptions\/[a-f0-9-]{36}\/|arn:aws[a-z-]*:[a-z0-9-]+:[^:\s]*:\d{12}:)/i],
    ['deployment environment resource', /['"](?:Radius\.Core|Applications\.Core)\/environments@/i],
    ['state registry binding', /ghcr\.io\/[a-z0-9_.-]+\/[a-z0-9_.-]*radius-state[a-z0-9_.-]*/i],
    ['SAS or storage credential', /(?:[?&]sig=[A-Za-z0-9%+/=]{12,}|AccountKey=[A-Za-z0-9+/=]{16,})/i],
  ];
  for (const [rule, pattern] of rules) {
    if (pattern.test(text)) findings.push(rule);
  }
  // Declarations and references are allowed; literal credential assignments are not.
  const assignments = /(?:["']?)([A-Za-z_][A-Za-z0-9_.-]*)(?:["']?)\s*[:=]\s*(?:(["'])([^"'\r\n]*)\2|([^\s,}\r\n]+))/g;
  for (const match of text.matchAll(assignments)) {
    const key = match[1];
    const value = match[3] ?? match[4];
    const sensitive = /(?:password|passwd|client[_-]?secret|api[_-]?key|access[_-]?token|(?:^|[_-])(?:token|secret)|secret[_-]?(?:key|access[_-]?key)|connection[_-]?string)$/i.test(key);
    const binding = /^(?:AZURE_(?:CLIENT_ID|TENANT_ID|SUBSCRIPTION_ID|RESOURCE_GROUP|AKS_CLUSTER_NAME)|AWS_(?:ROLE_ARN|ACCOUNT_ID)|RADIUS_(?:STATE_REGISTRY|VPC_ID|SUBNET_IDS)|KUBERNETES_NAMESPACE|subscriptionId|tenantId|roleArn)$/i.test(key);
    if (!sensitive && !binding) continue;
    if (!value || /^(?:null|undefined|true|false|\{|\[)$/.test(value) ||
        /^(?:<[^>]+>|\$\{[^}]+\}|\$\([^)]+\)|\$\{\{.+\}\})$/.test(value) ||
        /^(?:process\.env\.|os\.environ|env\.|secrets\.|vars\.)/.test(value)) continue;
    if (file.endsWith('.bicep') && !match[2] && /^[A-Za-z_][A-Za-z0-9_]*(?:[.(]|$)/.test(value)) continue;
    if (!match[2] && /\.[A-Za-z_]/.test(value) && !binding) continue;
    findings.push(binding ? 'literal deployment binding' : 'literal credential assignment');
  }
  if (/\bparam\s+environment\s+string\s*=/.test(text)) findings.push('environment parameter has a default');
  if (/\benvironment\s*:\s*['"][^'"]+['"]/.test(text)) findings.push('literal environment selection');
  if (/@secure\s*\(\s*\)(?:(?!\bparam\b)[\s\S])*?\bparam\s+\w+\s+(?:string|object)\s*=/.test(text)) {
    findings.push('secure parameter has a default');
  }
  if (/\bkind\s*:\s*['"]?Secret\b/.test(text) && /^\s*(?:data|stringData)\s*:/m.test(text)) {
    findings.push('Kubernetes Secret values');
  }
  for (const match of text.matchAll(/\bresource\s+\w+\s+'Radius\.Security\/secrets@[^']+'\s*=\s*\{/g)) {
    const body = resourceBody(text, match.index + match[0].length - 1);
    if (/\bvalue\s*:\s*['"][^'"]+['"]/.test(body)) {
      findings.push('literal Radius Secret value');
    }
  }
  return [...new Set(findings)];
}

export function assertPortable(bytes, file) {
  const reason = excludedReason(file);
  if (reason) throw new Error(`Cannot package ${file}: ${reason}.`);
  const findings = inspectContent(bytes, file);
  if (findings.length) {
    throw new Error(`Cannot package ${file}: ${findings.join(', ')}. Values are withheld; replace them with consumer-supplied inputs.`);
  }
}

export async function assertPortableFile(bytes, file) {
  if (!file.endsWith('.tgz')) return assertPortable(bytes, file);
  const reason = excludedReason(file);
  if (reason) throw new Error(`Cannot package ${file}: ${reason}.`);
  let archive;
  try { archive = gunzipSync(bytes, { maxOutputLength: MAX_PACK_BYTES }); }
  catch { throw new Error(`Invalid or oversized gzip archive: ${file}.`); }
  // Inspect metadata and trailing text too, not just the entries the TAR reader exposes.
  const findings = inspectContent(Buffer.from(archive.toString('utf8').replaceAll('\0', '\n')), file);
  if (findings.length) throw new Error(`Cannot package ${file}: ${findings.join(', ')}. Values withheld.`);
  await new Promise((resolve, reject) => {
    const parser = new Parser({ strict: true });
    const names = new Set();
    let count = 0;
    let failed = false;
    const fail = message => {
      if (failed) return;
      failed = true;
      reject(new Error(message));
      parser.abort(new Error('Archive inspection refused.'));
    };
    parser.on('error', () => fail(`Invalid TAR archive: ${file}.`));
    parser.on('ignoredEntry', () => fail(`Unsupported archive entry in ${file}.`));
    parser.on('entry', entry => {
      try {
        count++;
        if (count > 1000) throw new Error(`Too many archive entries in ${file}.`);
        const name = entry.path.replace(/^\.\//, '').replace(/\/$/, '');
        safePath(name);
        const key = name.toLowerCase();
        if (names.has(key)) throw new Error(`Duplicate archive entry in ${file}.`);
        names.add(key);
        if (!['File', 'Directory'].includes(entry.type) || entry.size > MAX_FILE_BYTES) {
          throw new Error(`Unsupported or oversized archive entry in ${file}.`);
        }
        if (excludedReason(name)) throw new Error(`Forbidden archive entry in ${file}.`);
        const chunks = [];
        entry.on('data', chunk => chunks.push(chunk));
        entry.on('end', () => {
          if (failed || entry.type === 'Directory') return;
          try { assertPortable(Buffer.concat(chunks), `${file}/${name}`); }
          catch { fail(`Non-portable or non-text archive contents in ${file}; inspect its members for credentials and unsupported assets.`); }
        });
        entry.resume();
      } catch {
        entry.resume();
        fail(`Unsafe, oversized, or unsupported archive entry in ${file}.`);
      }
    });
    parser.on('end', () => {
      if (!count) fail(`Empty TAR archive: ${file}.`);
      else if (!failed) resolve();
    });
    try { parser.end(archive); }
    catch { fail(`Invalid TAR archive: ${file}.`); }
  });
}

export function dependencies(bytes, file) {
  if (!file.endsWith('.bicep') && path.posix.basename(file) !== 'bicepconfig.json') return [];
  const text = textOf(bytes, file);
  const references = [];
  if (file.endsWith('.bicep')) {
    for (const pattern of [
      /\bmodule\s+\w+\s+'([^']+)'/g,
      /\bfrom\s+'([^']+)'/g,
      /\bextension\s+'([^']+)'/g,
      /\bload(?:Text|Json|Yaml|FileAsBase64)Content\s*\(\s*'([^']+)'/g,
      /\bloadFileAsBase64\s*\(\s*'([^']+)'/g,
    ]) {
      for (const match of text.matchAll(pattern)) references.push(match[1]);
    }
  }
  if (path.posix.basename(file) === 'bicepconfig.json') {
    let config;
    try { config = JSON.parse(text); } catch { throw new Error(`Invalid JSON: ${file}.`); }
    for (const reference of Object.values(config.extensions ?? {})) {
      if (typeof reference === 'string') references.push(reference);
    }
  }
  return references.flatMap(reference => {
    if (/^(?:br:|ts:)/.test(reference)) return [];
    if (reference.includes('${') || /^(?:[a-z][a-z0-9+.-]*:|\/)/i.test(reference)) {
      throw new Error(`Non-portable local dependency in ${file}; use a repository-relative path or a pinned registry reference.`);
    }
    const resolved = path.posix.normalize(path.posix.join(path.posix.dirname(file), reference));
    return [safePath(resolved)];
  });
}
