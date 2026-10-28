import { lstat, mkdir, mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { cloneRevision, repositoryURL, resolveRevision } from './git.mjs';
import { hash, installPack, loadPack, planCreate, writePack } from './pack.mjs';
import { readRegular, safePath } from './policy.mjs';

const DEFAULT_MANIFEST = '.radpack/radpack.json';
const catalogURL = new URL('../catalog.json', import.meta.url);

function parseJSON(bytes) {
  try { return JSON.parse(bytes.toString('utf8')); }
  catch { throw new Error('Invalid pack discovery JSON. Contents withheld.'); }
}

export function parsePackURL(value) {
  let url;
  try { url = new URL(value); } catch { throw new Error('Pack override must be a local directory or a GitHub tree URL.'); }
  if (url.protocol !== 'https:' || url.hostname !== 'github.com' || url.username ||
      url.password || url.search || url.hash || url.port) throw new Error('Use a credential-free GitHub tree URL for the pack.');
  const parts = url.pathname.split('/').filter(Boolean).map(part => decodeURIComponent(part));
  if (parts.length < 5 || parts[2] !== 'tree') throw new Error('Pack URL must be https://github.com/owner/repo/tree/<ref>/<directory>.');
  const repository = repositoryURL(`${parts[0]}/${parts[1]}`);
  const ref = parts[3];
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(ref) || ref.includes('..')) {
    throw new Error('Use a single-segment ref or exact commit SHA in a pack URL.');
  }
  return { repository, ref, directory: safePath(parts.slice(4).join('/')) };
}

async function localDirectory(value) {
  try {
    const stat = await lstat(value);
    if (stat.isSymbolicLink()) throw new Error('Pack directory cannot be a symbolic link.');
    return stat.isDirectory();
  } catch (error) {
    if (error.code === 'ENOENT' || error.code === 'ENOTDIR') return false;
    throw error;
  }
}

function validateAssetManifest(value) {
  const keys = ['formatVersion', 'kind', 'name', 'application', 'radiusVersion', 'assets'];
  if (!value || typeof value !== 'object' ||
      Object.keys(value).sort().join() !== keys.sort().join() ||
      value.formatVersion !== 1 || value.kind !== 'in-repository' ||
      !Array.isArray(value.assets) || value.assets.length > 1000 ||
      !value.assets.includes(value.application)) {
    throw new Error('Invalid in-repository manifest. Expected kind "in-repository" and an explicit assets list containing the application.');
  }
  for (const asset of value.assets) safePath(asset);
  return value;
}

async function repositoryPack(checkout, commit, repository, manifestPath, temp) {
  const file = await readRegular(checkout, manifestPath);
  if (!file) return null;
  const descriptor = validateAssetManifest(parseJSON(file.bytes));
  const plan = await planCreate({
    source: checkout, base: commit, upstream: repository, name: descriptor.name,
    radiusVersion: descriptor.radiusVersion, include: descriptor.assets,
  });
  if (plan.manifest.application !== descriptor.application) throw new Error('Manifest application does not match the discovered application model.');
  if (plan.blockers.length) throw new Error(`In-repository assets are not portable: ${plan.blockers.join('; ')}`);
  const directory = path.join(temp, 'materialized-pack');
  await writePack(plan, directory, plan.approval);
  return {
    directory,
    source: { kind: 'in-repository', repository, commit, path: manifestPath, manifestSha256: hash(file.bytes) },
  };
}

/**
 * Remote resolution stays in disposable checkouts. Only installPack owns the
 * consumer's destination; neither discovery nor a failed lookup can modify it.
 */
export async function installApplication(target, options = {}, report = () => {}) {
  if (Boolean(options.latest) !== Boolean(options.adapt)) {
    throw new Error('--latest and --adapt must be used together. Latest source requires an explicit Radius adaptation handoff.');
  }
  if (options.pack && options.packPath) throw new Error('Use --pack or --pack-path, not both.');
  if (options.packPath) safePath(options.packPath);
  const isLocal = await localDirectory(target);
  if (isLocal && (options.pack || options.packPath)) throw new Error('Pack overrides require an application repository target.');
  if (!isLocal && (target.startsWith('.') || path.isAbsolute(target))) throw new Error('Local pack directory does not exist.');
  const temp = await mkdtemp(path.join(tmpdir(), 'radpack-discovery-'));
  try {
    let repository = isLocal ? null : repositoryURL(target);
    let resolved;
    let head;
    if (isLocal) {
      const pack = await loadPack(target);
      repository = pack.manifest.upstream.repository;
      resolved = { directory: target, source: { kind: 'local', manifestSha256: pack.digest } };
    } else if (options.pack) {
      resolved = await resolveOverride(options.pack, temp);
    } else {
      head = await resolveRevision(repository);
      const checkout = path.join(temp, 'application');
      await mkdir(checkout);
      await cloneRevision(checkout, repository, head);
      const manifestPath = options.packPath
        ? `${safePath(options.packPath)}/radpack.json` : DEFAULT_MANIFEST;
      resolved = await repositoryPack(checkout, head, repository, manifestPath, temp);
      if (!resolved && options.packPath) throw new Error('No manifest found at --pack-path; catalog fallback is disabled for an explicit override.');
      if (!resolved) {
        const catalog = parseJSON(await readFile(options.catalog ?? catalogURL));
        if (!catalog || catalog.formatVersion !== 1 || !catalog.applications ||
            typeof catalog.applications !== 'object' || Array.isArray(catalog.applications)) throw new Error('Invalid Radpack catalog.');
        const key = repository.slice('https://github.com/'.length, -4).toLowerCase();
        const entry = catalog.applications[key];
        if (!entry) throw new Error(`No Radpack found for ${key}. Add .radpack/radpack.json upstream or supply --pack.`);
        if (typeof entry !== 'string') throw new Error('Catalog entry must be a GitHub tree URL.');
        const location = parsePackURL(entry);
        if (!/^[a-f0-9]{40}$/.test(location.ref)) throw new Error('Catalog entries must pin the pack repository to a full commit SHA.');
        resolved = await resolveOverride(entry, temp);
      }
    }
    const pack = await loadPack(resolved.directory);
    if (pack.manifest.upstream.repository.toLowerCase() !== repository.toLowerCase()) {
      throw new Error('Selected pack belongs to a different application repository.');
    }
    const selectedCommit = options.latest ? (head ?? await resolveRevision(repository)) : pack.manifest.upstream.commit;
    const directory = options.directory ?? path.basename(new URL(repository).pathname, '.git');
    report(`Application: ${repository}\nReference commit: ${pack.manifest.upstream.commit}\nSelected commit: ${selectedCommit}`);
    report(`Pack source: ${JSON.stringify(resolved.source)}\nOnly install packs from publishers you trust.`);
    const output = await installPack(resolved.directory, directory, {
      selectedCommit, adapt: Boolean(options.adapt), packSource: resolved.source,
    });
    return {
      directory: output,
      status: options.adapt ? 'adaptation-required' : 'installed',
      referenceCommit: pack.manifest.upstream.commit,
      selectedCommit,
      packSource: resolved.source,
    };
  } finally {
    await rm(temp, { recursive: true, force: true });
  }
}

async function resolveOverride(value, temp) {
  if (await localDirectory(value)) {
    const pack = await loadPack(value);
    return { directory: value, source: { kind: 'local', manifestSha256: pack.digest } };
  }
  if (value.startsWith('.') || path.isAbsolute(value)) throw new Error('Local pack override directory does not exist.');
  const location = parsePackURL(value);
  const commit = await resolveRevision(location.repository, location.ref);
  const checkout = path.join(temp, 'pack-source');
  await mkdir(checkout);
  await cloneRevision(checkout, location.repository, commit);
  // Walk the manifest path before loadPack/realpath so a directory symlink
  // cannot redirect discovery outside this checkout.
  if (!(await readRegular(checkout, `${location.directory}/radpack.json`))) throw new Error('Pack URL does not contain radpack.json.');
  const directory = path.join(checkout, location.directory);
  const pack = await loadPack(directory);
  return {
    directory,
    source: { kind: 'github', repository: location.repository, commit, path: location.directory, manifestSha256: pack.digest },
  };
}
