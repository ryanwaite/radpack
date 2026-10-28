import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { appendFile, chmod, cp, lstat, mkdir, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { promisify } from 'node:util';
import { gzipSync } from 'node:zlib';
import { create, Header } from 'tar';
import test from 'node:test';
import { main } from '../src/cli.mjs';
import { git, repositoryURL, resolveRevision } from '../src/git.mjs';
import { installApplication, parsePackURL } from '../src/discovery.mjs';
import { hash, installPack, loadPack, planCreate, validateManifest, verifyInstallation, writePack } from '../src/pack.mjs';
import { assertPortable, assertPortableFile, dependencies, excludedReason, inspectContent, safePath } from '../src/policy.mjs';

const exec = promisify(execFile);
const cli = fileURLToPath(new URL('../bin/radpack.mjs', import.meta.url));
const app = `extension radius
param environment string
resource app 'Radius.Core/applications@2025-08-01-preview' = {
  name: 'fixture'
  properties: {
    environment: environment
  }
}
`;
const config = '{"experimentalFeaturesEnabled":{"extensibility":true},"extensions":{"radius":"br:example.invalid/radius:0.60.0"}}\n';
const write = async (root, file, content) => {
  await mkdir(path.dirname(path.join(root, file)), { recursive: true });
  await writeFile(path.join(root, file), content);
};

async function fixture(t, { upstreamModel = false } = {}) {
  const root = await mkdtemp(path.join(tmpdir(), 'radpack-test-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const source = path.join(root, 'source');
  await mkdir(source);
  await git(source, ['init', '--quiet', '--template=']);
  await git(source, ['config', 'core.autocrlf', 'false']);
  await git(source, ['remote', 'add', 'origin', 'https://github.com/radpack-fixture/app.git']);
  await write(source, 'app.js', 'console.log("upstream");\n');
  await write(source, 'obsolete.txt', 'remove me\n');
  await write(source, '.gitignore', '.env\nignored.txt\n');
  if (upstreamModel) {
    await write(source, '.radius/app.bicep', app);
    await write(source, '.radius/bicepconfig.json', config);
  }
  await git(source, ['add', '.']);
  await git(source, ['-c', 'user.name=Radpack tests', '-c', 'user.email=tests@example.invalid', 'commit', '--quiet', '-m', 'Fixture baseline']);
  const base = await git(source, ['rev-parse', 'HEAD']);
  if (!upstreamModel) {
    await write(source, '.radius/app.bicep', app);
    await write(source, '.radius/bicepconfig.json', config);
  }
  const options = { source, base };
  const pack = path.join(root, 'pack');
  const destination = path.join(root, 'installed');
  const gitConfig = path.join(root, 'gitconfig');
  await writeFile(gitConfig, `[url "${pathToFileURL(source).href}"]\n\tinsteadOf = https://github.com/radpack-fixture/app.git\n[protocol "file"]\n\tallow = always\n[core]\n\tautocrlf = false\n`);
  async function localTransport(run) {
    const saved = process.env.GIT_CONFIG_GLOBAL;
    process.env.GIT_CONFIG_GLOBAL = gitConfig;
    try { return await run(); }
    finally {
      if (saved === undefined) delete process.env.GIT_CONFIG_GLOBAL;
      else process.env.GIT_CONFIG_GLOBAL = saved;
    }
  }
  async function publish(extra = {}) {
    const plan = await planCreate({ ...options, ...extra });
    assert.deepEqual(plan.blockers, []);
    await writePack(plan, pack, plan.approval);
    return plan;
  }
  return { root, source, base, options, pack, destination, localTransport, publish, gitConfig };
}

test('create -> inspect -> install -> verify reproduces additions, changes, deletions and modes', async t => {
  const f = await fixture(t);
  await write(f.source, 'app.js', 'console.log("prepared");\n');
  await write(f.source, 'start.sh', '#!/bin/sh\nprintf "hello\\n"\n');
  await chmod(path.join(f.source, 'start.sh'), 0o755);
  await rm(path.join(f.source, 'obsolete.txt'));
  await write(f.source, '.radius/radius-environment.bicep', 'author environment data');
  await write(f.source, '.radius/app-graph.json', '{"deployment":"author"}');
  await write(f.source, 'notes.txt', 'not selected');
  const plan = await f.publish({ include: ['app.js', 'obsolete.txt', 'start.sh'] });
  assert.ok(plan.excluded.some(entry => entry.path === '.radius/radius-environment.bicep'));
  assert.ok(plan.unselected.includes('notes.txt'));
  assert.deepEqual(plan.manifest.files.map(file => [file.path, file.operation]), [
    ['.radius/app.bicep', 'add'], ['.radius/bicepconfig.json', 'add'],
    ['app.js', 'replace'], ['obsolete.txt', 'delete'], ['start.sh', 'add'],
  ]);
  const loaded = await loadPack(f.pack);
  assert.equal(loaded.manifest.upstream.commit, f.base);
  await f.localTransport(() => installPack(f.pack, f.destination));
  assert.equal(await readFile(path.join(f.destination, 'app.js'), 'utf8'), 'console.log("prepared");\n');
  await assert.rejects(lstat(path.join(f.destination, 'obsolete.txt')), { code: 'ENOENT' });
  await assert.rejects(lstat(path.join(f.destination, '.radius/radius-environment.bicep')), { code: 'ENOENT' });
  const verified = await verifyInstallation(f.pack, f.destination);
  assert.equal(verified.files, 5);
  assert.equal(await git(f.destination, ['rev-parse', 'HEAD']), f.base);
});

test('unchanged upstream-owned models are retained, not needlessly copied', async t => {
  const f = await fixture(t, { upstreamModel: true });
  const plan = await f.publish();
  assert.ok(plan.manifest.files.every(file => file.operation === 'retain'));
  assert.equal(plan.payloads.size, 0);
  await f.localTransport(() => installPack(f.pack, f.destination));
});

test('local modules, loaded files and nearest Bicep config are captured', async t => {
  const f = await fixture(t);
  await write(f.source, '.radius/app.bicep', `${app}\nmodule worker './modules/worker.bicep' = { name: 'worker' }\n`);
  await write(f.source, '.radius/modules/worker.bicep', "var settings = loadTextContent('./settings.txt')\n");
  await write(f.source, '.radius/modules/settings.txt', 'portable settings\n');
  const plan = await f.publish();
  assert.ok(plan.manifest.files.some(file => file.path === '.radius/modules/settings.txt'));
  await f.localTransport(() => installPack(f.pack, f.destination));
});

test('root model and root compiler configuration are supported', async t => {
  const f = await fixture(t);
  await rm(path.join(f.source, '.radius'), { recursive: true });
  await write(f.source, 'app.bicep', app);
  await write(f.source, 'bicepconfig.json', config);
  const plan = await f.publish();
  assert.equal(plan.manifest.application, 'app.bicep');
  await f.localTransport(() => installPack(f.pack, f.destination));
});

test('dry run writes nothing and approval becomes invalid when selected bytes change', async t => {
  const f = await fixture(t);
  const first = await planCreate(f.options);
  await assert.rejects(lstat(f.pack), { code: 'ENOENT' });
  await write(f.source, '.radius/app.bicep', `${app}\n`);
  const second = await planCreate(f.options);
  assert.notEqual(first.approval, second.approval);
  await assert.rejects(writePack(second, f.pack, first.approval), /Approval does not match/);
  await assert.rejects(lstat(f.pack), { code: 'ENOENT' });
});

test('committed preparation changes are compared with the original baseline too', async t => {
  const f = await fixture(t);
  await write(f.source, 'app.js', 'committed preparation\n');
  await git(f.source, ['add', '.']);
  await git(f.source, ['-c', 'user.name=Radpack tests', '-c', 'user.email=tests@example.invalid', 'commit', '--quiet', '-m', 'Prepared app']);
  const plan = await f.publish({ include: ['app.js'] });
  assert.equal(plan.manifest.files.find(file => file.path === 'app.js').operation, 'replace');
  await f.localTransport(() => installPack(f.pack, f.destination));
});

test('unselected application changes are never silently exported', async t => {
  const f = await fixture(t);
  await write(f.source, 'app.js', 'local adaptation\n');
  const plan = await f.publish();
  assert.ok(plan.unselected.includes('app.js'));
  await f.localTransport(() => installPack(f.pack, f.destination));
  assert.equal(await readFile(path.join(f.destination, 'app.js'), 'utf8'), 'console.log("upstream");\n');
});

test('unsupported unselected paths are excluded without preventing an otherwise portable pack', async t => {
  const f = await fixture(t);
  await write(f.source, '\u6587\u6863.md', 'unrelated documentation');
  const plan = await f.publish();
  assert.ok(plan.excluded.some(file => file.path === '\u6587\u6863.md'));
});

test('forbidden paths remain forbidden even when explicitly included', async t => {
  const f = await fixture(t);
  await write(f.source, '.env', 'PASSWORD=not-a-real-password\n');
  const plan = await planCreate({ ...f.options, include: ['.env'] });
  assert.match(plan.blockers.join(), /environment values/);
  await assert.rejects(writePack(plan, f.pack, plan.approval), /blocked/);
  await assert.rejects(lstat(f.pack), { code: 'ENOENT' });
});

test('sensitive contents block publication without printing their values', async t => {
  const f = await fixture(t);
  const secret = `ghp_${'X'.repeat(36)}`;
  await write(f.source, '.radius/app.bicep', `${app}\nvar token = '${secret}'\n`);
  const output = [];
  const code = await main(['create', '--source', f.source, '--base', f.base, '--dry-run'], {
    log: value => output.push(value), error: value => output.push(value),
  });
  assert.equal(code, 1);
  assert.match(output.join('\n'), /GitHub token/);
  assert.ok(!output.join('\n').includes(secret));
  await assert.rejects(lstat(f.pack), { code: 'ENOENT' });
});

test('file limits, missing dependencies, and invalid archives fail explicitly', async t => {
  const f = await fixture(t);
  await write(f.source, '.radius/app.bicep', `${app}\nmodule missing './absent.bicep' = { name: 'missing' }\n`);
  assert.match((await planCreate(f.options)).blockers.join(), /Missing local dependency/);
  await write(f.source, '.radius/app.bicep', app);
  await write(f.source, '.radius/bicepconfig.json', '{"extensions":{"customTypes":"./custom-types.tgz"}}');
  await write(f.source, '.radius/custom-types.tgz', Buffer.from([0, 255, 1]));
  assert.match((await planCreate(f.options)).blockers.join(), /Invalid or oversized gzip/);
  await write(f.source, '.radius/custom-types.tgz', Buffer.alloc(8 * 1024 * 1024 + 1));
  assert.match((await planCreate(f.options)).blockers.join(), /8 MiB/);
});

test('local custom-type archives are inspected and preserved byte-for-byte', async t => {
  const f = await fixture(t);
  const types = path.join(f.root, 'types');
  await write(types, 'types.json', '{"types":[]}\n');
  await create({ cwd: types, gzip: true, file: path.join(f.source, '.radius/custom-types.tgz') }, ['types.json']);
  await write(f.source, '.radius/bicepconfig.json', '{"extensions":{"customTypes":"./custom-types.tgz"}}');
  const plan = await f.publish();
  assert.ok(plan.manifest.files.some(file => file.path === '.radius/custom-types.tgz'));
  await f.localTransport(() => installPack(f.pack, f.destination));
  assert.deepEqual(await readFile(path.join(f.source, '.radius/custom-types.tgz')),
    await readFile(path.join(f.destination, '.radius/custom-types.tgz')));
});

function archiveEntry(name, bytes, type = 'File') {
  const header = new Header({ path: name, mode: 0o644, size: bytes.length, type });
  const block = Buffer.alloc(512);
  header.encode(block);
  return gzipSync(Buffer.concat([block, bytes, Buffer.alloc((512 - bytes.length % 512) % 512), Buffer.alloc(1024)]));
}

test('archive policy rejects traversal, links, binaries, credentials and malformed archives', async () => {
  for (const bytes of [
    archiveEntry('../outside.json', Buffer.from('{}')),
    archiveEntry('/outside.json', Buffer.from('{}')),
    archiveEntry('.git/config', Buffer.from('{}')),
    archiveEntry('types.json', Buffer.alloc(0), 'SymbolicLink'),
    archiveEntry('types.json', Buffer.from([0, 255])),
    archiveEntry('data.json', Buffer.from('{"password":"do-not-export"}')),
    archiveEntry('.radius-credentials.json', Buffer.from('{}')),
    gzipSync(Buffer.from('not a tar archive')),
    gzipSync(Buffer.alloc(33 * 1024 * 1024)),
  ]) await assert.rejects(assertPortableFile(bytes, '.radius/custom-types.tgz'));
});

test('case collisions and file/directory overlaps are refused', async t => {
  const f = await fixture(t);
  const plan = await planCreate(f.options);
  const duplicate = structuredClone(plan.manifest);
  duplicate.files.push({ ...duplicate.files[0], path: '.radius/App.bicep' });
  assert.throws(() => validateManifest(duplicate), /case-colliding/);
  const overlap = structuredClone(plan.manifest);
  overlap.files.push({ ...overlap.files[0], path: '.radius' });
  assert.throws(() => validateManifest(overlap), /overlap/);
});

test('corrupt or extra payloads fail before a destination is created', async t => {
  const f = await fixture(t);
  await f.publish();
  await write(f.pack, 'overlay/.radius/app.bicep', 'tampered\n');
  await assert.rejects(installPack(f.pack, f.destination), /modified payload/);
  await assert.rejects(lstat(f.destination), { code: 'ENOENT' });
  await write(f.pack, 'overlay/.radius/app.bicep', app);
  await write(f.pack, 'credentials.txt', 'unexpected');
  await assert.rejects(loadPack(f.pack), /Undeclared/);
});

test('changing a payload and its checksum still cannot bypass credential policy', async t => {
  const f = await fixture(t);
  await f.publish();
  const manifest = JSON.parse(await readFile(path.join(f.pack, 'radpack.json'), 'utf8'));
  const malicious = `${app}\nvar configuration = { password: 'do-not-export' }\n`;
  manifest.files.find(file => file.path === '.radius/app.bicep').after.sha256 = hash(malicious);
  await write(f.pack, 'radpack.json', JSON.stringify(manifest));
  await write(f.pack, 'overlay/.radius/app.bicep', malicious);
  await assert.rejects(loadPack(f.pack), /credential assignment/);
});

test('incorrect upstream preconditions roll back only the new destination', async t => {
  const f = await fixture(t);
  await write(f.source, 'app.js', 'adaptation\n');
  await f.publish({ include: ['app.js'] });
  const manifest = JSON.parse(await readFile(path.join(f.pack, 'radpack.json'), 'utf8'));
  manifest.files.find(file => file.path === 'app.js').before.sha256 = '0'.repeat(64);
  await write(f.pack, 'radpack.json', JSON.stringify(manifest));
  await f.localTransport(() => assert.rejects(installPack(f.pack, f.destination), /precondition/));
  await assert.rejects(lstat(f.destination), { code: 'ENOENT' });
  assert.equal(await readFile(path.join(f.source, 'app.js'), 'utf8'), 'adaptation\n');
});

test('existing output and installation directories are untouched', async t => {
  const f = await fixture(t);
  const plan = await f.publish();
  await assert.rejects(writePack(plan, f.pack, plan.approval), /already exists/);
  await mkdir(f.destination);
  await write(f.destination, 'mine.txt', 'keep this');
  await assert.rejects(installPack(f.pack, f.destination), /already exists/);
  assert.equal(await readFile(path.join(f.destination, 'mine.txt'), 'utf8'), 'keep this');
});

test('verification catches payload edits, ignored leftovers, upstream edits and receipt changes', async t => {
  const f = await fixture(t);
  await f.publish();
  await f.localTransport(() => installPack(f.pack, f.destination));
  await write(f.destination, '.radius/app.bicep', 'edited');
  await assert.rejects(verifyInstallation(f.pack, f.destination), /differs/);
  await write(f.destination, '.radius/app.bicep', app);
  await write(f.destination, 'ignored.txt', 'leftover state');
  await assert.rejects(verifyInstallation(f.pack, f.destination), /undeclared/);
  await rm(path.join(f.destination, 'ignored.txt'));
  await write(f.destination, 'app.js', 'upstream edited');
  await assert.rejects(verifyInstallation(f.pack, f.destination), /undeclared/);
  await write(f.destination, 'app.js', 'console.log("upstream");\n');
  await write(f.destination, '.radpack/install.json', '{}');
  await assert.rejects(verifyInstallation(f.pack, f.destination), /receipt/);
});

test('symlink payloads and symlink source parents are refused', { skip: process.platform === 'win32' }, async t => {
  const f = await fixture(t);
  await f.publish();
  await rm(path.join(f.pack, 'overlay/.radius/app.bicep'));
  await symlink(path.join(f.source, '.radius/app.bicep'), path.join(f.pack, 'overlay/.radius/app.bicep'));
  await assert.rejects(loadPack(f.pack), /Symbolic links/);
  await rm(path.join(f.source, '.radius'), { recursive: true });
  await symlink(f.pack, path.join(f.source, '.radius'));
  await assert.rejects(planCreate(f.options), /Symbolic links/);
});

test('malicious manifest operations and arbitrary lifecycle hooks are rejected', async t => {
  const f = await fixture(t);
  const plan = await planCreate(f.options);
  for (const mutate of [
    value => { value.hooks = { install: 'execute-me' }; },
    value => { value.formatVersion = 999; },
    value => { value.name = 123; },
    value => { value.files[0].after.sha256 = ['0'.repeat(64)]; },
    value => { value.files[0].path = '../outside'; },
    value => { value.files[0].path = '.git/hooks/post-checkout'; },
    value => { value.files[0].path = '.radius-credentials.json'; },
    value => { value.upstream.repository = 'https://token@github.com/owner/repo.git'; },
    value => { value.files[0].operation = 'shell'; },
    value => { value.files[0].after.mode = '120000'; },
  ]) {
    const manifest = structuredClone(plan.manifest);
    mutate(manifest);
    assert.throws(() => validateManifest(manifest));
  }
});

test('failed revision fetch is reported without leaving an installation', async t => {
  const f = await fixture(t);
  await f.publish();
  const manifest = JSON.parse(await readFile(path.join(f.pack, 'radpack.json'), 'utf8'));
  manifest.upstream.commit = '0'.repeat(40);
  await write(f.pack, 'radpack.json', JSON.stringify(manifest));
  await f.localTransport(() => assert.rejects(installPack(f.pack, f.destination), /Git fetch failed/));
  await assert.rejects(lstat(f.destination), { code: 'ENOENT' });
});

test('inherited Git directory settings cannot redirect repository operations', async t => {
  const f = await fixture(t);
  const old = process.env.GIT_DIR;
  process.env.GIT_DIR = path.join(f.root, 'not-the-source');
  try {
    const plan = await planCreate(f.options);
    assert.equal(plan.manifest.upstream.commit, f.base);
  } finally {
    if (old === undefined) delete process.env.GIT_DIR;
    else process.env.GIT_DIR = old;
  }
});

test('published payload changes after review are rejected', async t => {
  const f = await fixture(t);
  const plan = await planCreate(f.options);
  plan.payloads.set('.radius/app.bicep', Buffer.from('changed'));
  await assert.rejects(writePack(plan, f.pack, plan.approval), /Payload changed/);
  await assert.rejects(lstat(f.pack), { code: 'ENOENT' });
});

test('installation rejects an undeclared dependency added by a tampered manifest', async t => {
  const f = await fixture(t);
  await write(f.source, '.radius/app.bicep', `${app}\nvar data = loadTextContent('../app.js')\n`);
  await f.publish();
  const manifest = JSON.parse(await readFile(path.join(f.pack, 'radpack.json'), 'utf8'));
  manifest.files = manifest.files.filter(file => file.path !== 'app.js');
  await write(f.pack, 'radpack.json', JSON.stringify(manifest));
  await f.localTransport(() => assert.rejects(installPack(f.pack, f.destination), /Undeclared or missing/));
  await assert.rejects(lstat(f.destination), { code: 'ENOENT' });
});

test('CLI supports reviewed export, inspect and real local-Git installation', async t => {
  const f = await fixture(t);
  const args = ['create', '--source', f.source, '--base', f.base, '--output', f.pack];
  const preview = await exec(process.execPath, [cli, ...args, '--dry-run']);
  const approval = /Review SHA: ([a-f0-9]{64})/.exec(preview.stdout)[1];
  await exec(process.execPath, [cli, ...args, '--approve', approval]);
  const inspected = await exec(process.execPath, [cli, 'inspect', f.pack]);
  assert.equal(JSON.parse(inspected.stdout).manifest.name, 'app');
  const installed = await exec(process.execPath, [cli, 'install', f.pack, '--directory', f.destination], {
    env: { ...process.env, GIT_CONFIG_GLOBAL: f.gitConfig },
  });
  assert.match(installed.stdout, /No environments, credentials/);
  const verified = await exec(process.execPath, [cli, 'verify', f.pack, '--directory', f.destination]);
  assert.match(verified.stdout, /Verified 2/);
  await assert.rejects(exec(process.execPath, [cli, 'verify', f.pack]), error => {
    assert.match(error.stderr, /requires --directory/);
    return true;
  });
});

test('CLI rejects unknown arguments without echoing possibly sensitive values', async () => {
  await assert.rejects(main(['create', '--password', 'do-not-print']), error => {
    assert.ok(!error.message.includes('do-not-print'));
    return true;
  });
  await assert.rejects(main(['create', '--approve', 'x', '--dry-run']), /not both/);
  await assert.rejects(main(['run']), /Unknown command/);
});

test('repository normalization rejects credentials, queries and arbitrary transports', () => {
  assert.equal(repositoryURL('owner/app'), 'https://github.com/owner/app.git');
  assert.equal(repositoryURL('git@github.com:owner/app.git'), 'https://github.com/owner/app.git');
  for (const value of ['file:///tmp/repo', 'ext::command', 'https://user:secret@github.com/owner/app',
    'https://github.com/owner/app?token=x', 'https://example.com/owner/app']) {
    assert.throws(() => repositoryURL(value));
  }
});

test('path rules and environment exclusion boundaries are portable', () => {
  for (const file of ['../x', '/x', 'a/../../x', '.git/config', 'a\\.git\\config',
    'CON', 'a/NUL.txt', 'x:y', 'a//b', 'a/./b', 'a/b.', 'a/\nsecret',
    '.git~1/config', '.git\u200c/hooks/post-checkout']) {
    assert.throws(() => safePath(file));
  }
  for (const file of ['.radius/radius-environment.bicep', '.radius/radius-recipe-pack.bicep',
    '.radius-target-kubeconfig', '.radius-credentials.json', '.rad/config.yaml',
    '.radius/.staging-test/app.bicep', '.aws/credentials', 'terraform.tfstate',
    'prod.bicepparam', '.github/workflows/deploy.yml', '.env', '.radpack/install.json',
    '.npmrc', 'secrets.json', '.docker/config.json']) {
    assert.ok(excludedReason(file), file);
  }
  assert.equal(excludedReason('.radius/custom-recipe-pack.bicep'), null);
  assert.equal(excludedReason('.radius/app.origin.json'), null);
});

test('secret checks recognize common representations without rejecting declarations', () => {
  for (const [file, content] of [
    ['app.js', "const config = { password: 'value-not-for-export' };"],
    ['settings.json', '{"apiKey":"value-not-for-export"}'],
    ['settings.json', '{"token":"value-not-for-export"}'],
    ['settings.json', '{"subscriptionId":"author-subscription"}'],
    ['settings.yml', 'AWS_SECRET_ACCESS_KEY: value-not-for-export'],
    ['settings.txt', '-----BEGIN PRIVATE KEY-----'],
    ['settings.txt', 'postgres://person:password@host/db'],
    ['app.bicep', "@secure()\nparam password string = 'bad-default'"],
    ['app.bicep', `${app}\nresource creds 'Radius.Security/secrets@2025-08-01-preview' = { properties: { data: { password: { value: 'bad' } } } }`],
    ['secret.yaml', 'kind: Secret\nstringData:\n  anything: value\n'],
    ['app.bicep', "param environment string = 'author-env'"],
    ['app.bicep', "resource env 'Radius.Core/environments@2025-08-01-preview' = {}"],
  ]) assert.ok(inspectContent(Buffer.from(content), file).length, file);
  const portable = `${app}
@secure()
param brokerPassword string
param queue string = 'orders'
resource creds 'Radius.Security/secrets@2025-08-01-preview' = {
  properties: { data: { password: { value: brokerPassword } } }
}
resource worker 'Radius.Compute/containers@2025-08-01-preview' = {
  properties: { env: { QUEUE: { value: 'orders' } } }
}
`;
  assert.deepEqual(inspectContent(Buffer.from(portable), 'app.bicep'), []);
  assert.doesNotThrow(() => assertPortable(Buffer.from('PASSWORD=<provide-at-deployment>'), '.env.example'));
});

test('dependency discovery refuses traversal and includes literal local imports', () => {
  const text = Buffer.from("import { setting } from './shared.bicep'\nvar cfg = loadJsonContent('./settings.json')\n");
  assert.deepEqual(dependencies(text, '.radius/app.bicep'), ['.radius/shared.bicep', '.radius/settings.json']);
  assert.throws(() => dependencies(Buffer.from("module x '../../outside.bicep' = {}"), '.radius/app.bicep'), /Unsafe/);
  assert.throws(() => dependencies(Buffer.from("module x '/outside.bicep' = {}"), '.radius/app.bicep'), /Non-portable/);
});

async function commitFiles(source, files) {
  await git(source, ['add', '--', ...files]);
  await git(source, ['-c', 'user.name=Radpack tests', '-c', 'user.email=tests@example.invalid',
    'commit', '--quiet', '-m', 'Fixture update']);
  return git(source, ['rev-parse', 'HEAD']);
}

function assetManifest() {
  return {
    formatVersion: 1, kind: 'in-repository', name: 'fixture',
    application: '.radius/app.bicep', radiusVersion: null,
    assets: ['.radius/app.bicep', '.radius/bicepconfig.json'],
  };
}

async function packPublisher(f) {
  const publisher = path.join(f.root, 'publisher');
  await mkdir(publisher);
  await git(publisher, ['init', '--quiet', '--template=']);
  await cp(f.pack, path.join(publisher, 'contrib/app'), { recursive: true });
  const commit = await commitFiles(publisher, ['contrib/app']);
  await appendFile(f.gitConfig, `\n[url "${pathToFileURL(publisher).href}"]\n\tinsteadOf = https://github.com/radpack-fixture/packs.git\n`);
  return { publisher, commit, url: `https://github.com/radpack-fixture/packs/tree/${commit}/contrib/app` };
}

test('repository install discovers in-repository assets without duplicated payloads', async t => {
  const f = await fixture(t);
  await write(f.source, '.radpack/radpack.json', JSON.stringify(assetManifest()));
  const commit = await commitFiles(f.source, ['.radius', '.radpack/radpack.json']);
  const result = await f.localTransport(() => installApplication('radpack-fixture/app', { directory: f.destination }));
  assert.equal(result.status, 'installed');
  assert.equal(result.selectedCommit, commit);
  assert.equal(result.packSource.kind, 'in-repository');
  assert.equal(result.packSource.commit, commit);
  const pack = await loadPack(path.join(f.destination, '.radpack/pack'));
  assert.ok(pack.manifest.files.every(file => file.operation === 'retain'));
  assert.equal((await verifyInstallation(undefined, f.destination)).status, 'installed');
});

test('explicit in-repository pack path is supported without catalog fallback', async t => {
  const f = await fixture(t);
  await write(f.source, 'integrations/radius/radpack.json', JSON.stringify(assetManifest()));
  await commitFiles(f.source, ['.radius', 'integrations']);
  await f.localTransport(() => installApplication('radpack-fixture/app', {
    directory: f.destination, packPath: 'integrations/radius',
  }));
  await f.localTransport(() => assert.rejects(installApplication('radpack-fixture/app', {
    directory: path.join(f.root, 'missing'), packPath: 'does-not-exist',
  }), /fallback is disabled/));
});

test('catalog resolves an external pack and installs its reference, not current application HEAD', async t => {
  const f = await fixture(t);
  await f.publish();
  const publisher = await packPublisher(f);
  await write(f.source, 'app.js', 'upstream has evolved\n');
  const head = await commitFiles(f.source, ['app.js']);
  const catalog = path.join(f.root, 'catalog.json');
  await writeFile(catalog, JSON.stringify({
    formatVersion: 1, applications: { 'radpack-fixture/app': publisher.url },
  }));
  const result = await f.localTransport(() => installApplication('radpack-fixture/app', {
    directory: f.destination, catalog,
  }));
  assert.notEqual(head, result.selectedCommit);
  assert.equal(result.selectedCommit, f.base);
  assert.equal(result.packSource.commit, publisher.commit);
  assert.equal(result.packSource.path, 'contrib/app');
  assert.equal(await readFile(path.join(f.destination, 'app.js'), 'utf8'), 'console.log("upstream");\n');
  await verifyInstallation(undefined, f.destination);
});

test('explicit pack override takes precedence over a malformed in-repository manifest', async t => {
  const f = await fixture(t);
  await f.publish();
  await write(f.source, '.radpack/radpack.json', '{broken');
  await commitFiles(f.source, ['.radpack/radpack.json']);
  const result = await f.localTransport(() => installApplication('radpack-fixture/app', {
    directory: f.destination, pack: f.pack,
  }));
  assert.equal(result.selectedCommit, f.base);
  await f.localTransport(() => assert.rejects(installApplication('radpack-fixture/app', {
    directory: path.join(f.root, 'no-fallback'),
  }), /Invalid pack discovery JSON/));
});

test('GitHub pack overrides resolve branch refs and record immutable publisher provenance', async t => {
  const f = await fixture(t);
  await f.publish();
  const { publisher, commit } = await packPublisher(f);
  await git(publisher, ['branch', 'published']);
  const result = await f.localTransport(() => installApplication('radpack-fixture/app', {
    directory: f.destination, pack: 'https://github.com/radpack-fixture/packs/tree/published/contrib/app',
  }));
  assert.equal(result.packSource.commit, commit);
  assert.equal(result.status, 'installed');
});

test('latest adaptation preserves application conflicts and both model revisions', async t => {
  const f = await fixture(t);
  await write(f.source, 'app.js', 'pack adaptation\n');
  await f.publish({ include: ['app.js', 'obsolete.txt'] });
  await write(f.source, 'app.js', 'new upstream implementation\n');
  await write(f.source, '.radius/app.bicep', `${app}\n// upstream also gained a model\n`);
  const latest = await commitFiles(f.source, ['app.js', '.radius']);
  const result = await f.localTransport(() => installApplication('radpack-fixture/app', {
    directory: f.destination, pack: f.pack, latest: true, adapt: true,
  }));
  assert.equal(result.status, 'adaptation-required');
  assert.equal(result.selectedCommit, latest);
  assert.equal(await readFile(path.join(f.destination, 'app.js'), 'utf8'), 'new upstream implementation\n');
  assert.match(await readFile(path.join(f.destination, '.radius/app.bicep'), 'utf8'), /upstream also/);
  assert.equal(await readFile(path.join(f.destination, '.radpack/reference/.radius/app.bicep'), 'utf8'), app);
  assert.equal(await readFile(path.join(f.destination, '.radpack/reference/app.js'), 'utf8'), 'pack adaptation\n');
  const request = JSON.parse(await readFile(path.join(f.destination, '.radpack/adaptation.json'), 'utf8'));
  assert.deepEqual(request.conflicts, ['.radius/app.bicep', 'app.js']);
  assert.equal(request.referenceCommit, f.base);
  assert.equal(request.selectedCommit, latest);
  assert.equal(request.deploymentStatus, 'unverified');
  assert.match(await readFile(path.join(f.destination, '.radpack/ADAPT.md'), 'utf8'), /assembled application/);
  assert.equal((await verifyInstallation(undefined, f.destination)).status, 'adaptation-required');
  await write(f.destination, '.radius/app.bicep', app);
  await assert.rejects(verifyInstallation(undefined, f.destination), /differs/);
});

test('latest retains a deleted required file as a conflict rather than restoring old source', async t => {
  const f = await fixture(t);
  await f.publish({ include: ['app.js'] });
  await rm(path.join(f.source, 'app.js'));
  await commitFiles(f.source, ['app.js']);
  await f.localTransport(() => installApplication(f.pack, { directory: f.destination, latest: true, adapt: true }));
  await assert.rejects(lstat(path.join(f.destination, 'app.js')), { code: 'ENOENT' });
  const request = JSON.parse(await readFile(path.join(f.destination, '.radpack/adaptation.json'), 'utf8'));
  assert.ok(request.conflicts.includes('app.js'));
  assert.equal((await verifyInstallation(f.pack, f.destination)).status, 'adaptation-required');
});

test('latest applies nonconflicting adaptations but never claims model validation', async t => {
  const f = await fixture(t);
  await write(f.source, 'app.js', 'pack adaptation\n');
  await rm(path.join(f.source, 'obsolete.txt'));
  await f.publish({ include: ['app.js', 'obsolete.txt'] });
  await write(f.source, 'app.js', 'console.log("upstream");\n');
  await write(f.source, 'obsolete.txt', 'remove me\n');
  await write(f.source, 'NEWS.md', 'unrelated upstream changes\n');
  await commitFiles(f.source, ['NEWS.md']);
  const result = await f.localTransport(() => installApplication(f.pack, { directory: f.destination, latest: true, adapt: true }));
  assert.equal(await readFile(path.join(f.destination, 'app.js'), 'utf8'), 'pack adaptation\n');
  await assert.rejects(lstat(path.join(f.destination, 'obsolete.txt')), { code: 'ENOENT' });
  assert.equal(result.status, 'adaptation-required');
  const request = JSON.parse(await readFile(path.join(f.destination, '.radpack/adaptation.json'), 'utf8'));
  assert.deepEqual(request.conflicts, []);
});

test('no pack, mismatched application, and mutable catalog entries fail clearly', async t => {
  const f = await fixture(t);
  await f.publish();
  await f.localTransport(() => assert.rejects(installApplication('radpack-fixture/app', { directory: f.destination }), /No Radpack found/));
  await assert.rejects(installApplication('unrelated/app', { directory: f.destination, pack: f.pack }), /different application/);
  const catalog = path.join(f.root, 'catalog.json');
  await writeFile(catalog, JSON.stringify({
    formatVersion: 1, applications: { 'radpack-fixture/app': 'https://github.com/radpack-fixture/packs/tree/main/contrib/app' },
  }));
  await f.localTransport(() => assert.rejects(installApplication('radpack-fixture/app', {
    directory: f.destination, catalog,
  }), /must pin/));
  await assert.rejects(lstat(f.destination), { code: 'ENOENT' });
});

test('latest requires explicit adaptation and conflicting discovery options are rejected', async () => {
  await assert.rejects(installApplication('owner/repo', { latest: true }), /used together/);
  await assert.rejects(installApplication('owner/repo', { adapt: true }), /used together/);
  await assert.rejects(installApplication('owner/repo', { pack: 'x', packPath: 'y' }), /not both/);
});

test('pack URL parsing rejects unsafe paths, credentials and ambiguous branch syntax', () => {
  assert.deepEqual(parsePackURL('https://github.com/owner/packs/tree/v1.0/contrib/app'), {
    repository: 'https://github.com/owner/packs.git', ref: 'v1.0', directory: 'contrib/app',
  });
  for (const url of [
    'https://user:token@github.com/owner/packs/tree/main/app',
    'https://github.com/owner/packs/tree/main/app?token=value',
    'http://github.com/owner/packs/tree/main/app',
    'https://example.com/owner/packs/tree/main/app',
    'https://github.com/owner/packs/tree/main/%2e%2e%2foutside',
    'https://github.com/owner/packs/tree/main/.git/hooks',
    'https://github.com/owner/packs/tree/feature%2fbranch/app',
  ]) assert.throws(() => parsePackURL(url));
});

test('remote resolution rejects ambiguous branch/tag refs and peels annotated tags', async t => {
  const f = await fixture(t);
  await git(f.source, ['branch', 'ambiguous']);
  await git(f.source, ['tag', 'ambiguous']);
  await git(f.source, ['-c', 'user.name=Radpack tests', '-c', 'user.email=tests@example.invalid',
    'tag', '-a', 'release', '-m', 'Release']);
  await f.localTransport(async () => {
    await assert.rejects(resolveRevision('https://github.com/radpack-fixture/app.git', 'ambiguous'), /both a branch and a tag/);
    assert.equal(await resolveRevision('https://github.com/radpack-fixture/app.git', 'release'), f.base);
  });
});

test('metadata tampering fails verification and metadata never enters a new export', async t => {
  const f = await fixture(t);
  await f.publish();
  await f.localTransport(() => installApplication(f.pack, { directory: f.destination, latest: true, adapt: true }));
  const plan = await planCreate({ source: f.destination, base: f.base });
  assert.ok(plan.excluded.some(file => file.path === '.radpack/ADAPT.md'));
  assert.ok(plan.manifest.files.every(file => !file.path.startsWith('.radpack/')));
  await write(f.destination, '.radpack/adaptation.json', '{}');
  await assert.rejects(verifyInstallation(undefined, f.destination), /metadata changed/);
});

test('CLI latest installation reports an explicit handoff and self-contained verification', async t => {
  const f = await fixture(t);
  await f.publish();
  const output = await exec(process.execPath, [cli, 'install', 'radpack-fixture/app', '--pack', f.pack,
    '--directory', f.destination, '--latest', '--adapt'], {
    env: { ...process.env, GIT_CONFIG_GLOBAL: f.gitConfig },
  });
  assert.match(output.stdout, /adaptation-required/);
  assert.match(output.stdout, /no agent or Radius tool was executed/);
  const verified = await exec(process.execPath, [cli, 'verify', '--directory', f.destination]);
  assert.match(verified.stdout, /Status: adaptation-required/);
});

test('upstream Radius environment artifacts are refused even when absent from the overlay', async t => {
  const f = await fixture(t);
  await f.publish();
  await write(f.source, '.radius/radius-environment.bicep', 'author-specific environment');
  await commitFiles(f.source, ['.radius/radius-environment.bicep']);
  await f.localTransport(() => assert.rejects(installApplication(f.pack, {
    directory: f.destination, latest: true, adapt: true,
  }), /Upstream contains non-portable Radius environment material/));
  await assert.rejects(lstat(f.destination), { code: 'ENOENT' });
});

test('an in-repository manifest takes precedence over catalog entries', async t => {
  const f = await fixture(t);
  await write(f.source, '.radpack/radpack.json', JSON.stringify(assetManifest()));
  await commitFiles(f.source, ['.radius', '.radpack/radpack.json']);
  const catalog = path.join(f.root, 'bad-catalog.json');
  await writeFile(catalog, 'not json');
  const result = await f.localTransport(() => installApplication('radpack-fixture/app', {
    directory: f.destination, catalog,
  }));
  assert.equal(result.packSource.kind, 'in-repository');
});

test('in-repository descriptors cannot include credentials or escape the checkout', async t => {
  const f = await fixture(t);
  const descriptor = assetManifest();
  descriptor.assets.push('../outside');
  await write(f.source, '.radpack/radpack.json', JSON.stringify(descriptor));
  await commitFiles(f.source, ['.radius', '.radpack/radpack.json']);
  await f.localTransport(() => assert.rejects(installApplication('radpack-fixture/app', {
    directory: f.destination,
  }), /Unsafe pack path/));
  descriptor.assets.pop();
  descriptor.assets.push('.env');
  await write(f.source, '.radpack/radpack.json', JSON.stringify(descriptor));
  await commitFiles(f.source, ['.radpack/radpack.json']);
  await f.localTransport(() => assert.rejects(installApplication('radpack-fixture/app', {
    directory: f.destination,
  }), /not portable/));
  await assert.rejects(lstat(f.destination), { code: 'ENOENT' });
});

test('upstream already containing a pack adaptation is not reported as a conflict', async t => {
  const f = await fixture(t);
  await write(f.source, 'app.js', 'same adaptation\n');
  await f.publish({ include: ['app.js'] });
  await commitFiles(f.source, ['app.js']);
  await f.localTransport(() => installApplication(f.pack, {
    directory: f.destination, latest: true, adapt: true,
  }));
  const request = JSON.parse(await readFile(path.join(f.destination, '.radpack/adaptation.json'), 'utf8'));
  assert.deepEqual(request.conflicts, []);
  assert.ok(!request.appliedPaths.includes('app.js'));
});

test('CLI installation defaults to the application repository name', async t => {
  const f = await fixture(t);
  await f.publish();
  await exec(process.execPath, [cli, 'install', 'radpack-fixture/app', '--pack', f.pack], {
    cwd: f.root, env: { ...process.env, GIT_CONFIG_GLOBAL: f.gitConfig },
  });
  assert.equal((await verifyInstallation(undefined, path.join(f.root, 'app'))).status, 'installed');
});

test('legacy v1 receipts remain verifiable with an explicit original pack', async t => {
  const f = await fixture(t);
  await f.publish();
  await f.localTransport(() => installPack(f.pack, f.destination));
  const receiptPath = path.join(f.destination, '.radpack/install.json');
  const receipt = JSON.parse(await readFile(receiptPath, 'utf8'));
  await rm(path.join(f.destination, '.radpack/pack'), { recursive: true });
  await writeFile(receiptPath, JSON.stringify({
    formatVersion: 1, manifest: receipt.manifest, packManifestSha256: receipt.packManifestSha256,
  }));
  assert.equal((await verifyInstallation(f.pack, f.destination)).status, 'installed');
});
