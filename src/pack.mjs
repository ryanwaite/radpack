import { createHash } from 'node:crypto';
import { chmod, lstat, mkdir, readdir, realpath, rm, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { cloneRevision, git, repositoryURL, treeAt } from './git.mjs';
import { adaptationInstructions } from './adaptation.mjs';
import {
  assertPortableFile, dependencies, excludedReason, MAX_FILE_BYTES, MAX_PACK_BYTES,
  readRegular, RECEIPT, safePath,
} from './policy.mjs';

const SHA = /^[a-f0-9]{64}$/;
const COMMIT = /^[a-f0-9]{40}$/;
const NAME = /^[a-z0-9][a-z0-9-]{0,62}$/;
const MANIFEST = 'radpack.json';
const DEFAULT_ASSETS = ['app.origin.json', 'custom-types.yaml', 'custom-recipe-pack.bicep'];

export const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const json = value => `${JSON.stringify(value, null, 2)}\n`;
const fingerprint = file => file ? { sha256: hash(file.bytes), mode: file.mode } : null;
const same = (a, b) => a?.sha256 === b?.sha256 && a?.mode === b?.mode;

function exactKeys(value, keys, label) {
  if (!value || typeof value !== 'object' || Array.isArray(value) ||
      Object.keys(value).sort().join('|') !== [...keys].sort().join('|')) {
    throw new Error(`Invalid ${label}: unexpected or missing fields.`);
  }
}

function validateFingerprint(value) {
  if (value === null) return;
  exactKeys(value, ['sha256', 'mode'], 'file fingerprint');
  if (typeof value.sha256 !== 'string' || !SHA.test(value.sha256) || !['100644', '100755'].includes(value.mode)) {
    throw new Error('Invalid file checksum or mode.');
  }
}

export function validateManifest(manifest) {
  exactKeys(manifest, ['formatVersion', 'name', 'upstream', 'application', 'radiusVersion', 'files'], 'manifest');
  if (manifest.formatVersion !== 1) throw new Error('Unsupported pack format version.');
  if (typeof manifest.name !== 'string' || !NAME.test(manifest.name)) throw new Error('Invalid pack name.');
  exactKeys(manifest.upstream, ['repository', 'commit'], 'upstream');
  if (repositoryURL(manifest.upstream.repository) !== manifest.upstream.repository ||
      typeof manifest.upstream.commit !== 'string' ||
      !COMMIT.test(manifest.upstream.commit)) throw new Error('Upstream must have a canonical URL and full commit SHA.');
  if (manifest.radiusVersion !== null &&
      (typeof manifest.radiusVersion !== 'string' || !/^\d+\.\d+(?:\.\d+)?(?:-[a-zA-Z0-9.-]+)?$/.test(manifest.radiusVersion))) {
    throw new Error('Radius version must be an exact release version, or null when unspecified.');
  }
  if (!['app.bicep', '.radius/app.bicep'].includes(manifest.application)) {
    throw new Error('Application must be app.bicep or .radius/app.bicep.');
  }
  if (!Array.isArray(manifest.files) || !manifest.files.length || manifest.files.length > 1000) {
    throw new Error('A pack must declare between 1 and 1000 files.');
  }
  const seen = new Set();
  for (const file of manifest.files) {
    exactKeys(file, ['path', 'operation', 'before', 'after'], 'file operation');
    safePath(file.path);
    const reason = excludedReason(file.path);
    if (reason) throw new Error(`Forbidden pack path ${file.path}: ${reason}.`);
    const key = file.path.toLowerCase();
    if (seen.has(key)) throw new Error(`Duplicate or case-colliding path: ${file.path}.`);
    seen.add(key);
    validateFingerprint(file.before);
    validateFingerprint(file.after);
    const valid =
      (file.operation === 'add' && file.before === null && file.after !== null) ||
      (file.operation === 'replace' && file.before !== null && file.after !== null && !same(file.before, file.after)) ||
      (file.operation === 'delete' && file.before !== null && file.after === null) ||
      (file.operation === 'retain' && file.before !== null && same(file.before, file.after));
    if (!valid) throw new Error(`Invalid operation for ${file.path}.`);
  }
  for (const file of seen) {
    const parts = file.split('/');
    while (parts.pop() && parts.length) {
      if (seen.has(parts.join('/'))) throw new Error('File paths overlap a parent file.');
    }
  }
  if (!manifest.files.some(file => file.path === manifest.application && file.after)) {
    throw new Error('Pack does not contain its application definition.');
  }
  return manifest;
}

async function baseFile(source, tree, file) {
  const entry = tree.get(file);
  if (!entry) return null;
  if (entry.type !== 'blob' || !['100644', '100755'].includes(entry.mode)) {
    throw new Error(`Cannot package a symbolic link or submodule: ${file}.`);
  }
  return { bytes: await git(source, ['cat-file', 'blob', entry.object], { binary: true }), mode: entry.mode };
}

async function nearestConfig(source, file) {
  let directory = path.posix.dirname(file);
  for (;;) {
    const candidate = path.posix.join(directory, 'bicepconfig.json');
    if (await readRegular(source, candidate)) return candidate;
    if (directory === '.') return null;
    directory = path.posix.dirname(directory);
  }
}

async function radiusFiles(source) {
  const result = [];
  async function walk(relative) {
    const absolute = path.join(source, relative);
    let entries;
    try {
      const stat = await lstat(absolute);
      if (stat.isSymbolicLink()) throw new Error(`Symbolic links are not allowed: ${relative}.`);
      entries = await readdir(absolute, { withFileTypes: true });
    } catch (error) {
      if (error.code === 'ENOENT') return;
      throw error;
    }
    for (const entry of entries) {
      const file = `${relative}/${entry.name}`;
      if (result.length >= 1000) throw new Error('Too many Radius files to inspect.');
      try { safePath(file); }
      catch { result.push(file); continue; }
      if (entry.isDirectory() && !excludedReason(file)) await walk(file);
      else result.push(file);
    }
  }
  await walk('.radius');
  return result;
}

export async function planCreate(options) {
  const source = await realpath(options.source ?? '.');
  const top = await realpath(await git(source, ['rev-parse', '--show-toplevel']));
  if (source !== top) throw new Error('--source must be the Git repository root.');
  if (!COMMIT.test(options.base ?? '')) throw new Error('--base must be the full 40-character upstream commit SHA.');
  const resolved = await git(source, ['rev-parse', '--verify', `${options.base}^{commit}`]);
  if (resolved !== options.base) throw new Error('The baseline is not a commit.');
  const repository = repositoryURL(options.upstream ?? await git(source, ['config', '--get', 'remote.origin.url']));
  const name = options.name ?? path.basename(new URL(repository).pathname, '.git').toLowerCase().replace(/[^a-z0-9-]/g, '-');
  if (!NAME.test(name)) throw new Error('Pack name must be 1-63 lowercase letters, numbers, or hyphens.');
  const application = await readRegular(source, '.radius/app.bicep') ? '.radius/app.bicep' : 'app.bicep';
  if (!(await readRegular(source, application))) throw new Error('No application model found. Prepare and verify the application with Radius before creating a pack.');
  const selected = new Set([application]);
  for (const asset of DEFAULT_ASSETS) {
    const file = path.posix.join(path.posix.dirname(application), asset);
    if (await readRegular(source, file)) selected.add(file);
  }
  for (const file of options.include ?? []) selected.add(safePath(file));
  const tree = await treeAt(source, options.base);
  const payloads = new Map();
  const files = [];
  const blockers = [];
  let totalBytes = 0;
  // A growing Set visits newly discovered local dependencies as well.
  for (const file of selected) {
    if (selected.size > 1000) throw new Error('Too many selected files.');
    try {
      const reason = excludedReason(file);
      if (reason) throw new Error(`Cannot package ${file}: ${reason}.`);
      const original = await baseFile(source, tree, file);
      const current = await readRegular(source, file);
      if (!current && !original) throw new Error(`Selected file does not exist: ${file}.`);
      if (current) {
        await assertPortableFile(current.bytes, file);
        for (const dependency of dependencies(current.bytes, file)) {
          if (!(await readRegular(source, dependency))) throw new Error(`Missing local dependency of ${file}: ${dependency}.`);
          selected.add(dependency);
        }
        if (file.endsWith('.bicep')) {
          const config = await nearestConfig(source, file);
          if (config) selected.add(config);
        }
        totalBytes += current.bytes.length;
        if (totalBytes > MAX_PACK_BYTES) throw new Error('Selected files exceed the 32 MiB pack limit.');
      }
      const before = fingerprint(original);
      const after = fingerprint(current);
      const operation = !original ? 'add' : !current ? 'delete' : same(before, after) ? 'retain' : 'replace';
      files.push({ path: file, operation, before, after });
      if (current && operation !== 'retain') payloads.set(file, current.bytes);
    } catch (error) {
      blockers.push(error.message);
    }
  }
  files.sort((a, b) => a.path < b.path ? -1 : a.path > b.path ? 1 : 0);
  const manifest = {
    formatVersion: 1, name, upstream: { repository, commit: options.base },
    application, radiusVersion: options.radiusVersion ?? null, files,
  };
  if (!blockers.length) validateManifest(manifest);
  const changed = await git(source, ['diff', '--no-ext-diff', '--no-textconv', '--no-renames', '--name-only', '-z', options.base, '--'], { binary: true });
  const untracked = await git(source, ['ls-files', '--others', '--exclude-standard', '-z'], { binary: true });
  const candidates = new Set([
    ...changed.toString('utf8').split('\0').filter(Boolean),
    ...untracked.toString('utf8').split('\0').filter(Boolean),
    ...await radiusFiles(source),
  ]);
  const excluded = [];
  const unselected = [];
  for (const file of [...candidates].sort()) {
    if (selected.has(file)) continue;
    let reason;
    try { reason = excludedReason(file); }
    catch { reason = 'unsupported or unsafe export path'; }
    if (reason) excluded.push({ path: file, reason });
    else unselected.push(file);
  }
  const review = { manifest, excluded, unselected, blockers };
  const approval = hash(json(review));
  return { ...review, approval, payloads, source };
}

async function withNewDirectory(destination, operation) {
  const absolute = path.resolve(destination);
  await mkdir(path.dirname(absolute), { recursive: true });
  // mkdir without recursive is the exclusive reservation; an existing path is never reused.
  try {
    await mkdir(absolute);
  } catch (error) {
    if (error.code === 'EEXIST') throw new Error(`Destination already exists: ${absolute}. Choose a new directory.`);
    throw error;
  }
  try {
    return await operation(absolute);
  } catch (error) {
    try { await rm(absolute, { recursive: true }); }
    catch { throw new Error(`${error.message} Cleanup failed; inspect ${absolute} before retrying.`); }
    throw error;
  }
}

export async function writePack(plan, destination, approval) {
  if (plan.blockers.length) throw new Error('Export is blocked. Resolve the reported policy findings first.');
  const review = { manifest: plan.manifest, excluded: plan.excluded, unselected: plan.unselected, blockers: plan.blockers };
  if (approval !== hash(json(review))) throw new Error('Approval does not match the current inventory. Review create --dry-run again.');
  validateManifest(plan.manifest);
  for (const file of plan.manifest.files.filter(file => ['add', 'replace'].includes(file.operation))) {
    const bytes = plan.payloads.get(file.path);
    if (!bytes || hash(bytes) !== file.after.sha256) throw new Error('Payload changed after review.');
    await assertPortableFile(bytes, file.path);
  }
  return withNewDirectory(destination, async output => {
    for (const [file, bytes] of plan.payloads) {
      const target = path.join(output, 'overlay', file);
      await mkdir(path.dirname(target), { recursive: true });
      await writeFile(target, bytes, { flag: 'wx' });
    }
    await writeFile(path.join(output, MANIFEST), json(plan.manifest), { flag: 'wx' });
    await loadPack(output);
    return output;
  });
}

async function checkPackInventory(root, expected) {
  const found = new Set();
  async function walk(relative = '') {
    for (const entry of await readdir(path.join(root, relative), { withFileTypes: true })) {
      const file = relative ? `${relative}/${entry.name}` : entry.name;
      safePath(file);
      if (entry.isSymbolicLink()) throw new Error(`Pack contains a symbolic link: ${file}.`);
      if (entry.isDirectory()) {
        if (![...expected].some(item => item.startsWith(`${file}/`))) throw new Error(`Undeclared directory in pack: ${file}.`);
        await walk(file);
      } else if (entry.isFile() && expected.has(file)) found.add(file);
      else throw new Error(`Undeclared or unsupported file in pack: ${file}.`);
    }
  }
  await walk();
  if (found.size !== expected.size) throw new Error('Pack payload is incomplete.');
}

export async function loadPack(directory) {
  const root = await realpath(directory);
  const file = await readRegular(root, MANIFEST);
  if (!file) throw new Error('Pack has no radpack.json.');
  let parsed;
  try { parsed = JSON.parse(file.bytes.toString('utf8')); }
  catch { throw new Error('Invalid pack manifest JSON. Contents withheld.'); }
  const manifest = validateManifest(parsed);
  const payloads = new Map();
  const expected = new Set([MANIFEST]);
  let totalBytes = 0;
  for (const entry of manifest.files) {
    if (!['add', 'replace'].includes(entry.operation)) continue;
    const file = `overlay/${entry.path}`;
    expected.add(file);
    const payload = await readRegular(root, file);
    if (!payload || hash(payload.bytes) !== entry.after.sha256) throw new Error(`Missing or modified payload: ${entry.path}.`);
    await assertPortableFile(payload.bytes, entry.path);
    totalBytes += payload.bytes.length;
    if (totalBytes > MAX_PACK_BYTES) throw new Error('Payload exceeds the 32 MiB pack limit.');
    payloads.set(entry.path, payload.bytes);
  }
  await checkPackInventory(root, expected);
  return { manifest, payloads, digest: hash(file.bytes) };
}

async function verifyDependencies(destination, manifest) {
  const declared = new Set(manifest.files.filter(file => file.after).map(file => file.path));
  let totalBytes = 0;
  for (const file of manifest.files.filter(file => file.after)) {
    const current = await readRegular(destination, file.path);
    if (!current) throw new Error(`Declared file is missing: ${file.path}.`);
    totalBytes += current.bytes.length;
    if (totalBytes > MAX_PACK_BYTES) throw new Error('Declared files exceed the 32 MiB pack limit.');
    await assertPortableFile(current.bytes, file.path);
    const required = dependencies(current.bytes, file.path);
    if (file.path.endsWith('.bicep')) {
      const config = await nearestConfig(destination, file.path);
      if (config) required.push(config);
    }
    for (const dependency of required) {
      if (!declared.has(dependency) || !(await readRegular(destination, dependency))) {
        throw new Error(`Undeclared or missing local dependency of ${file.path}: ${dependency}.`);
      }
    }
  }
}

export async function installPack(packDirectory, destination, options = {}) {
  const pack = await loadPack(packDirectory);
  const { manifest, payloads } = pack;
  const selectedCommit = options.selectedCommit ?? manifest.upstream.commit;
  if (typeof selectedCommit !== 'string' || !COMMIT.test(selectedCommit)) throw new Error('Selected revision must be a full commit SHA.');
  if (selectedCommit !== manifest.upstream.commit && !options.adapt) {
    throw new Error('A different application revision requires adaptation.');
  }
  return withNewDirectory(destination, async output => {
    await cloneRevision(output, manifest.upstream.repository, selectedCommit);
    if (selectedCommit !== manifest.upstream.commit) {
      await git(output, ['fetch', '--quiet', '--no-tags', '--depth=1', 'origin', manifest.upstream.commit]);
    }
    const tree = await treeAt(output, manifest.upstream.commit);
    const selectedTree = await treeAt(output, selectedCommit);
    if ([...selectedTree.keys()].some(file =>
      (file.toLowerCase() === '.radpack' || file.toLowerCase().startsWith('.radpack/')) &&
      file !== '.radpack/radpack.json')) {
      throw new Error('Upstream uses reserved Radpack installation metadata paths.');
    }
    for (const file of selectedTree.keys()) {
      const lower = file.toLowerCase();
      const radiusOwned = lower.startsWith('.radius/') ||
        lower.startsWith('.rad/') || lower.startsWith('.kube/') ||
        lower === '.radius-credentials.json' || lower === '.radius-target-kubeconfig';
      if (radiusOwned && excludedReason(file)) {
        throw new Error(`Upstream contains non-portable Radius environment material: ${JSON.stringify(file)}. Remove it from the selected source revision before distributing this application.`);
      }
    }
    // A checked-in asset manifest is allowed; installation metadata is not.
    if (selectedTree.has('.radpack/radpack.json')) await readRegular(output, '.radpack/radpack.json');
    const conflicts = [];
    const referenceAssets = new Map();
    const actions = [];
    let referenceBytes = 0;
    // Validate the reference pack and all checkout preconditions before writes.
    for (const file of manifest.files) {
      const original = await baseFile(output, tree, file.path);
      if (!same(fingerprint(original), file.before)) throw new Error(`Upstream precondition failed for ${file.path}.`);
      if (file.after) {
        const bytes = file.operation === 'retain' ? original.bytes : payloads.get(file.path);
        if (bytes.length > MAX_FILE_BYTES) throw new Error(`Reference asset exceeds the 8 MiB limit: ${file.path}.`);
        referenceBytes += bytes.length;
        if (referenceBytes > MAX_PACK_BYTES) throw new Error('Reference assets exceed the 32 MiB pack limit.');
        await assertPortableFile(bytes, file.path);
        referenceAssets.set(file.path, bytes);
      }
      const selected = await baseFile(output, selectedTree, file.path);
      const current = await readRegular(output, file.path);
      if (current && !selected) throw new Error(`Unexpected existing file: ${file.path}.`);
      if (selected && (!current || hash(current.bytes) !== hash(selected.bytes))) {
        throw new Error(`Checkout changed ${file.path}; Git filters or line-ending conversion are not supported.`);
      }
      if (options.adapt && !same(fingerprint(selected), file.before)) {
        if (!same(fingerprint(selected), file.after)) conflicts.push(file.path);
        // Even when upstream already has the desired bytes, do not overwrite it.
        continue;
      }
      actions.push(file);
    }
    for (const file of actions) {
      const target = path.join(output, file.path);
      if (file.operation === 'delete') await rm(target);
      else if (file.operation !== 'retain') {
        await mkdir(path.dirname(target), { recursive: true });
        await writeFile(target, payloads.get(file.path), { flag: file.operation === 'add' ? 'wx' : 'w' });
        await chmod(target, file.after.mode === '100755' ? 0o755 : 0o644);
      }
    }
    if (!options.adapt) await verifyDependencies(output, manifest);
    const files = [];
    for (const file of manifest.files) {
      const current = await readRegular(output, file.path);
      if (current) await assertPortableFile(current.bytes, file.path);
      files.push({ path: file.path, after: fingerprint(current) });
    }
    const metadata = [];
    async function writeMetadata(file, bytes) {
      const target = path.join(output, file);
      await mkdir(path.dirname(target), { recursive: true });
      await writeFile(target, bytes, { flag: 'wx' });
      metadata.push({ path: file, sha256: hash(bytes) });
    }
    // Keep an exact, locally inspectable pack after disposable discovery clones
    // have gone away. It is never registered as an executable agent skill.
    const manifestFile = await readRegular(packDirectory, MANIFEST);
    await writeMetadata('.radpack/pack/radpack.json', manifestFile.bytes);
    for (const [file, bytes] of payloads) await writeMetadata(`.radpack/pack/overlay/${file}`, bytes);
    if (options.adapt) {
      for (const [file, bytes] of referenceAssets) await writeMetadata(`.radpack/reference/${file}`, bytes);
      const changed = await git(output, ['diff', '--no-ext-diff', '--no-textconv', '--no-renames',
        '--name-only', '-z', manifest.upstream.commit, selectedCommit, '--'], { binary: true });
      await writeMetadata('.radpack/upstream-changes.json', json(changed.toString('utf8').split('\0').filter(Boolean)));
      const request = {
        formatVersion: 1, status: 'adaptation-required',
        repository: manifest.upstream.repository,
        referenceCommit: manifest.upstream.commit, selectedCommit,
        packSource: options.packSource ?? { kind: 'local', manifestSha256: pack.digest },
        conflicts, application: manifest.application,
        referenceAssets: [...referenceAssets.keys()],
        appliedPaths: actions.filter(file => file.operation !== 'retain').map(file => file.path),
        deploymentStatus: 'unverified',
      };
      await writeMetadata('.radpack/adaptation.json', json(request));
      await writeMetadata('.radpack/ADAPT.md', adaptationInstructions(request));
    }
    await writeFile(path.join(output, RECEIPT), json({
      formatVersion: 2, packManifestSha256: pack.digest, manifest,
      applicationCommit: selectedCommit,
      packSource: options.packSource ?? { kind: 'local', manifestSha256: pack.digest },
      status: options.adapt ? 'adaptation-required' : 'installed',
      files, metadata,
    }), { flag: 'wx' });
    await verifyInstallation(packDirectory, output);
    return output;
  });
}

export async function verifyInstallation(packDirectory, directory) {
  const root = await realpath(directory);
  const pack = await loadPack(packDirectory ?? path.join(root, '.radpack/pack'));
  if (await realpath(await git(root, ['rev-parse', '--show-toplevel'])) !== root) {
    throw new Error('Installation directory must be a Git repository root.');
  }
  if (repositoryURL(await git(root, ['config', '--get', 'remote.origin.url'])) !== pack.manifest.upstream.repository) {
    throw new Error('Installed origin differs from the pack upstream.');
  }
  const receiptFile = await readRegular(root, RECEIPT);
  if (!receiptFile) throw new Error('Installation receipt is missing.');
  let receipt;
  try { receipt = JSON.parse(receiptFile.bytes.toString('utf8')); }
  catch { throw new Error('Invalid installation receipt.'); }
  const legacy = receipt?.formatVersion === 1;
  exactKeys(receipt, legacy ? ['formatVersion', 'packManifestSha256', 'manifest'] :
    ['formatVersion', 'packManifestSha256', 'manifest', 'applicationCommit', 'packSource', 'status', 'files', 'metadata'], 'installation receipt');
  if ((!legacy && receipt.formatVersion !== 2) || receipt.packManifestSha256 !== pack.digest ||
      json(receipt.manifest) !== json(pack.manifest)) throw new Error('Installation receipt does not match this pack.');
  if (!legacy && (!['installed', 'adaptation-required'].includes(receipt.status) ||
      typeof receipt.applicationCommit !== 'string' || !COMMIT.test(receipt.applicationCommit) ||
      !Array.isArray(receipt.files) || !Array.isArray(receipt.metadata))) throw new Error('Invalid installation receipt.');
  const selectedCommit = legacy ? pack.manifest.upstream.commit : receipt.applicationCommit;
  if (await git(root, ['rev-parse', 'HEAD']) !== selectedCommit) throw new Error('Installed HEAD differs from the recorded application revision.');
  if (!legacy && receipt.status === 'installed' && selectedCommit !== pack.manifest.upstream.commit) {
    throw new Error('Matching installation receipt does not match the reference revision.');
  }
  const files = legacy ? pack.manifest.files : receipt.files;
  if (files.length !== pack.manifest.files.length ||
      files.some((file, index) => file.path !== pack.manifest.files[index].path)) throw new Error('Installation receipt inventory does not match the pack.');
  for (const file of files) {
    safePath(file.path);
    validateFingerprint(file.after);
    if (!legacy && receipt.status === 'installed' &&
        !same(file.after, pack.manifest.files.find(entry => entry.path === file.path).after)) {
      throw new Error('Matching installation fingerprints differ from the pack.');
    }
    const current = await readRegular(root, file.path);
    if (file.after === null) {
      if (current) throw new Error(`Deleted file reappeared: ${file.path}.`);
    } else if (!current || hash(current.bytes) !== file.after.sha256 ||
        (process.platform !== 'win32' && current.mode !== file.after.mode)) {
      throw new Error(`Installed file differs from the pack: ${file.path}.`);
    }
  }
  if (legacy || receipt.status === 'installed') await verifyDependencies(root, pack.manifest);
  const allowed = new Set(pack.manifest.files.map(file => file.path));
  if (!legacy) {
    for (const file of receipt.metadata) {
      safePath(file.path);
      if (!file.path.startsWith('.radpack/') || file.path === RECEIPT ||
          file.path === '.radpack/radpack.json' || allowed.has(file.path) ||
          typeof file.sha256 !== 'string' || !SHA.test(file.sha256)) throw new Error('Invalid metadata inventory in receipt.');
      const content = await readRegular(root, file.path);
      if (!content || hash(content.bytes) !== file.sha256) throw new Error(`Installation metadata changed: ${file.path}.`);
      allowed.add(file.path);
    }
    if (!allowed.has('.radpack/pack/radpack.json') ||
        (receipt.status === 'adaptation-required' && !allowed.has('.radpack/adaptation.json'))) {
      throw new Error('Receipt is missing required installation metadata.');
    }
    const snapshot = await loadPack(path.join(root, '.radpack/pack'));
    if (snapshot.digest !== pack.digest) throw new Error('Saved pack does not match the installation.');
  }
  const modified = await git(root, ['diff', '--no-ext-diff', '--no-textconv', '--no-renames', '--name-only', '-z', 'HEAD', '--'], { binary: true });
  // Include ignored files: deployment artifacts must not hide behind .gitignore during verification.
  const untracked = await git(root, ['ls-files', '--others', '-z'], { binary: true });
  const unexpected = [...new Set([...modified.toString('utf8').split('\0'), ...untracked.toString('utf8').split('\0')])]
    .filter(file => file && file !== RECEIPT && !allowed.has(file));
  if (unexpected.length) {
    throw new Error(`Installation contains undeclared changes or files: ${unexpected.map(file => JSON.stringify(file)).join(', ')}.`);
  }
  return {
    name: pack.manifest.name, files: pack.manifest.files.length, upstream: selectedCommit,
    status: legacy ? 'installed' : receipt.status,
  };
}
