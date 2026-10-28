import { execFile } from 'node:child_process';
import { devNull } from 'node:os';
import { promisify } from 'node:util';

const exec = promisify(execFile);

export async function git(cwd, args, { binary = false } = {}) {
  const env = {
    ...process.env,
    GIT_TERMINAL_PROMPT: '0',
    GIT_LFS_SKIP_SMUDGE: '1',
    GIT_PAGER: 'cat',
  };
  for (const key of ['GIT_DIR', 'GIT_WORK_TREE', 'GIT_INDEX_FILE', 'GIT_COMMON_DIR',
    'GIT_OBJECT_DIRECTORY', 'GIT_ALTERNATE_OBJECT_DIRECTORIES']) delete env[key];
  try {
    const result = await exec('git', [
      '-c', `core.hooksPath=${devNull}`,
      '-c', 'protocol.ext.allow=never',
      '-c', 'core.fsmonitor=false',
      '-c', 'core.autocrlf=false',
      ...args,
    ], {
      cwd,
      encoding: binary ? 'buffer' : 'utf8',
      maxBuffer: 32 * 1024 * 1024,
      env,
    });
    return binary ? result.stdout : result.stdout.trim();
  } catch (error) {
    // Git diagnostics can contain credential-bearing URLs or helper output.
    throw new Error(`Git ${args[0]} failed (${error.code ?? 'unknown'}). Check repository access and the requested revision; raw Git output was withheld.`);
  }
}

export function repositoryURL(value) {
  if (typeof value !== 'string') throw new Error('An upstream GitHub repository is required.');
  const match = /^(?:https:\/\/github\.com\/|git@github\.com:)?([A-Za-z0-9-]+)\/([A-Za-z0-9_.-]+?)(?:\.git)?$/.exec(value);
  if (!match || ['.', '..'].includes(match[2])) {
    throw new Error('Upstream must be owner/repo or a credential-free github.com repository URL.');
  }
  return `https://github.com/${match[1]}/${match[2]}.git`;
}

export async function treeAt(source, commit) {
  const output = await git(source, ['ls-tree', '-r', '-z', commit], { binary: true });
  const entries = new Map();
  for (const record of output.toString('utf8').split('\0').filter(Boolean)) {
    const match = /^(\d+) (\w+) ([a-f0-9]+)\t(.+)$/s.exec(record);
    if (!match) throw new Error('Cannot read the upstream Git tree.');
    entries.set(match[4], { mode: match[1], type: match[2], object: match[3] });
  }
  return entries;
}

export async function cloneRevision(destination, repository, commit) {
  await git(destination, ['init', '--quiet', '--template=']);
  await git(destination, ['remote', 'add', 'origin', repository]);
  await git(destination, ['fetch', '--quiet', '--no-tags', '--depth=1', 'origin', commit]);
  const actual = await git(destination, ['rev-parse', 'FETCH_HEAD']);
  if (actual !== commit) throw new Error('Fetched upstream revision does not match the pack.');
  await git(destination, ['checkout', '--quiet', '-b', 'radpack', commit]);
}

export async function resolveRevision(repository, ref = 'HEAD') {
  repositoryURL(repository);
  if (typeof ref !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9._/-]*$/.test(ref) ||
      ref.includes('..') || ref.endsWith('/')) throw new Error('Invalid Git revision selector.');
  if (/^[a-f0-9]{40}$/.test(ref)) return ref;
  const selectors = ref === 'HEAD' ? ['HEAD'] : [`refs/heads/${ref}`, `refs/tags/${ref}`, `refs/tags/${ref}^{}`];
  const lines = (await git(process.cwd(), ['ls-remote', repository, ...selectors])).split('\n').filter(Boolean);
  const records = lines.map(line => line.split('\t'));
  if (ref !== 'HEAD' && records.some(([, name]) => name === `refs/heads/${ref}`) &&
      records.some(([, name]) => name.startsWith(`refs/tags/${ref}`))) {
    throw new Error('Revision names both a branch and a tag; use an exact commit SHA.');
  }
  const selected = records.find(([, name]) => name.endsWith('^{}')) ?? records[0];
  if (!selected || !/^[a-f0-9]{40}$/.test(selected[0])) throw new Error('Requested remote revision was not found.');
  return selected[0];
}
