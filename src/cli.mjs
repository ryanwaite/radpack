import path from 'node:path';
import { parseArgs } from 'node:util';
import { loadPack, planCreate, verifyInstallation, writePack } from './pack.mjs';
import { installApplication } from './discovery.mjs';

const HELP = `Radpack - set up cloud-native apps, explore their architecture, and deploy to your cloud of choice with Radius

  radpack create --base <full-sha> [--source <repo>] [--output <pack-directory>]
                 [--upstream <owner/repo>] [--name <name>] [--include <file>]...
                 [--radius-version <version>] [--dry-run | --approve <review-sha>]
  radpack inspect <pack-directory>
  radpack install <owner/repo|github-url|pack-directory> [--directory <new-directory>]
                  [--pack <local-directory|github-tree-url> | --pack-path <repo-directory>]
                  [--latest --adapt]
  radpack verify [<pack-directory>] --directory <installed-directory>

Create previews the inventory without writing until --approve matches its review.
Output defaults to ./contrib/<pack-name>. Existing destinations are never reused.
Install never creates Radius environments, executes pack scripts, or deploys.
Install defaults to the pack's reference commit. --latest --adapt prepares a
Radius authoring handoff, NOT an automatically adapted or deployment-ready app.
Verify checks file reproduction, not Radius compilation or cloud functionality.
`;

const OPTIONS = {
  create: {
    source: { type: 'string' }, base: { type: 'string' },
    output: { type: 'string' }, upstream: { type: 'string' }, name: { type: 'string' },
    include: { type: 'string', multiple: true },
    'radius-version': { type: 'string' },
    'dry-run': { type: 'boolean' }, approve: { type: 'string' },
  },
  inspect: {},
  install: {
    directory: { type: 'string' }, pack: { type: 'string' }, 'pack-path': { type: 'string' },
    latest: { type: 'boolean' }, adapt: { type: 'boolean' },
  },
  verify: { directory: { type: 'string' } },
};

export async function main(args, io = console) {
  if (!args.length || args[0] === '--help' || args[0] === 'help') {
    io.log(HELP);
    return 0;
  }
  const [command, ...rest] = args;
  if (!Object.hasOwn(OPTIONS, command)) throw new Error('Unknown command. Use radpack --help.');
  let parsed;
  try {
    parsed = parseArgs({ args: rest, options: OPTIONS[command], allowPositionals: true, strict: true });
  } catch {
    throw new Error('Invalid command options. Use radpack --help. Supplied values withheld.');
  }
  const { values, positionals } = parsed;
  if (command === 'create') {
    if (positionals.length) throw new Error('create takes options, not positional arguments.');
    if (values.approve && values['dry-run']) throw new Error('Use --dry-run or --approve, not both.');
    const plan = await planCreate({
      source: values.source, base: values.base, upstream: values.upstream, name: values.name,
      include: values.include, radiusVersion: values['radius-version'],
    });
    const output = path.resolve(values.output ?? path.join('contrib', plan.manifest.name));
    io.log(`Pack: ${plan.manifest.name}\nUpstream: ${plan.manifest.upstream.repository}@${plan.manifest.upstream.commit}`);
    io.log(`Output: ${output}\nRadius release annotation: ${plan.manifest.radiusVersion ?? 'unspecified; inspect the included Bicep configuration'}`);
    io.log('\nSelected files:');
    for (const file of plan.manifest.files) io.log(`  ${file.operation.padEnd(7)} ${file.path}`);
    if (plan.excluded.length) {
      io.log('\nExcluded:');
      for (const file of plan.excluded) io.log(`  ${JSON.stringify(file.path)}: ${file.reason}`);
    }
    if (plan.unselected.length) {
      io.log('\nNot selected (use --include <file> for a required application change):');
      for (const file of plan.unselected) io.log(`  ${JSON.stringify(file)}`);
    }
    if (plan.blockers.length) {
      io.error('\nExport blocked:');
      for (const blocker of plan.blockers) io.error(`  ${blocker}`);
      return 1;
    }
    if (values.approve) {
      await writePack(plan, output, values.approve);
      io.log(`\nCreated ${output}. Cloud deployment status: unverified by Radpack.`);
    } else {
      io.log(`\nReview SHA: ${plan.approval}`);
      io.log('No files written. Review the selected file contents, then repeat the same command without --dry-run and add --approve <review-sha>.');
      io.log('Secret detection is best-effort, not a guarantee. No deployment verification is implied.');
    }
    return 0;
  }
  if ((command === 'verify' && positionals.length > 1) ||
      (command !== 'verify' && positionals.length !== 1)) {
    throw new Error(`${command} requires ${command === 'verify' ? 'at most' : 'exactly'} one target.`);
  }
  const packDirectory = positionals[0];
  if (command === 'inspect') {
    const pack = await loadPack(packDirectory);
    io.log(JSON.stringify({ manifestSha256: pack.digest, manifest: pack.manifest, deploymentStatus: 'unverified by Radpack' }, null, 2));
  } else {
    if (command === 'install') {
      const result = await installApplication(packDirectory, {
        directory: values.directory, pack: values.pack, packPath: values['pack-path'],
        latest: values.latest, adapt: values.adapt,
      }, message => io.log(message));
      if (result.status === 'adaptation-required') {
        io.log(`Prepared latest source in ${result.directory}.\nStatus: adaptation-required (not model-validated or deployment-verified).\nOpen this checkout in Copilot with the Radius tools and follow .radpack/ADAPT.md.\nConflicts and reference assets are preserved; no agent or Radius tool was executed.`);
      } else {
        io.log(`Installed and verified files in ${result.directory}.\nOpen this checkout in Radius Canvas, then create your own Radius environment before deploying.`);
      }
      io.log('No environments, credentials, workflows, or cloud resources were created.');
    } else {
      if (!values.directory) throw new Error(`${command} requires --directory.`);
      const result = await verifyInstallation(packDirectory, values.directory);
      io.log(`Verified ${result.files} declared files for ${result.name} at upstream ${result.upstream}.\nStatus: ${result.status}.\nThis verifies installation content, not adaptation, compilation, or deployment.`);
    }
  }
  return 0;
}
