# Radpack

**Set up cloud-native apps, explore their architecture, and deploy to your cloud
of choice with Radius.**

Radpack brings existing cloud native applications together with Radius application
graphs, reusable recipes, and tools. Install a pack, see how the application fits
together in the GitHub Copilot App with Radius Canvas, then use Radius to configure 
your environment and deploy.

Authors prepare and verify an application with Radius, then package it for others
to use. Radpack captures that **prepared Radius application** and installs it over
an exact revision of its original GitHub repository. It distributes the application
model, reusable recipes, and reviewed source adaptations, **not the author's
credentials or deployment environment**.

This is an early CLI with application-based discovery and local pack support.
It does not generate models, install the Radius plugin, create environments,
execute pack hooks, or deploy cloud resources. Latest-source installation prepares
a handoff for a human-guided Radius authoring session; it does not run an agent.
The bundled catalog is empty: no deployment-verified application packs ship yet.

## How it works

Radpack separates the application, its reusable Radius integration, and the
consumer's deployment environment:

```text
Application repository + reference commit
                   +
Radius model, recipes, and reviewed application adaptations
                   |
             radpack create
                   |
             Portable pack
                   |
             radpack install
                   |
  Application checkout + Radius integration + installation receipt
                   |
       Radius Canvas: inspect the application graph
                   |
       Consumer creates their own Radius environment
                   |
       Radius builds and deploys to the chosen provider
```

An author does the application-specific integration work once. Consumers install
that prepared integration without inheriting the author's cloud identity,
credentials, cluster, or deployment state. Radius, rather than Radpack, owns
model generation, graph rendering, recipe execution, and deployment.

There are two ways to distribute the integration:

- **External pack:** a directory containing `radpack.json` and an `overlay/`
  of new or modified files. The manifest records the original application's
  repository and commit, file operations, checksums, and executable modes.
  Deletions record only original fingerprints, not removed file contents.
- **In-repository integration:** a small `.radpack/radpack.json` descriptor points
  at existing application assets, usually in `.radius/`. Installation records
  these files as retained upstream content instead of requiring a second copy.

The application revision and the pack publisher's revision are separate.
Installation records both. Neither a reproducible installation nor an author-supplied
Radius version annotation proves that the application works on a cloud provider.

## Run from this repository

Requires Go 1.24 or newer to build and Git to operate on repositories. Radpack
builds into a native executable; running that executable requires neither Go nor
Node.js. The implementation uses only the Go standard library.

```bash
git clone https://github.com/ryanwaite/radpack.git
cd radpack
go build -o ./bin/radpack ./cmd/radpack
./bin/radpack --help
```

On Windows, build with `go build -o .\bin\radpack.exe .\cmd\radpack`, then run
`.\bin\radpack.exe --help`.

To install this checkout's command into your Go executable directory:

```bash
go install ./cmd/radpack
```

That directory is `GOBIN` when configured, otherwise the `bin` directory under
`GOPATH` (normally `$HOME/go/bin`). Add it to your shell's PATH if needed.
`go install` copies a compiled executable; rerun it after
changing the source. It does not modify PATH or publish a release.
The commands below assume `radpack` is on PATH; otherwise use the built
executable's path. No prebuilt release binaries are published yet.

## Install an application's Radpack

```bash
# Default: reproduce the pack's recorded application revision
radpack install Azure-Samples/aks-store-demo

# Override discovery with an external pack (local directory or GitHub tree URL)
radpack install Azure-Samples/aks-store-demo --pack ./contrib/aks-store-demo
radpack install Azure-Samples/aks-store-demo \
  --pack https://github.com/example/packs/tree/v1/contrib/aks-store-demo

# Use a manifest in a non-default directory of the application repository
radpack install Azure-Samples/aks-store-demo --pack-path integrations/radius

# Explicitly bring the integration forward through a Radius authoring handoff
radpack install Azure-Samples/aks-store-demo --latest --adapt
```

These are usage examples, not claims that an AKS Store pack is currently available.
The target may be `owner/repo`, a credential-free GitHub repository URL, or an
existing local pack directory. Use `./` for unambiguous local paths. An existing
directory takes precedence over a similarly spelled repository identifier.
The destination defaults to `./<application-repository-name>`; override it with
`--directory`. Existing destinations are never reused.

Discovery precedence:

1. Explicit `--pack` or `--pack-path` (mutually exclusive).
2. `.radpack/radpack.json` in the application's current remote default-branch commit.
3. An entry for the lowercase `owner/repo` in this CLI's bundled `catalog.json`.

A broken or non-portable in-repository manifest is an error, not permission to
silently fall back to another publisher. An explicit missing `--pack-path` also
fails without fallback. Discovery uses a temporary checkout and never modifies
the application repository.

External packs must identify the requested application repository. GitHub pack
URLs take the form `https://github.com/owner/repo/tree/<ref>/<directory>`.
Use an exact commit SHA when the branch name contains `/`; single-segment branches
and tags are also accepted for explicit overrides, resolved to a commit and
recorded before installation. Catalog entries require exact commit SHAs.
Arbitrary download URLs are not supported. Pack provenance is displayed before
installation. Choose publishers you trust; this is not signature verification.

### Keep the integration in the application repository

For an application whose model and compiler configuration are already committed,
the default `.radpack/radpack.json` can be:

```json
{
  "formatVersion": 1,
  "kind": "in-repository",
  "name": "my-app",
  "application": ".radius/app.bicep",
  "radiusVersion": null,
  "assets": [
    ".radius/app.bicep",
    ".radius/bicepconfig.json"
  ]
}
```

Paths are relative to the application repository root. Add any required custom
recipes, application instructions, or other portable assets to the list.
Standard companion assets and literal local dependencies are discovered as well.
The descriptor does not name its own commit: discovery resolves the commit
containing it and records that revision. See the [format reference](docs/pack-format.md)
for the external pack format, catalog entries, and installation receipts.

### Matching revision versus latest source

**Matching revision is the default.** External packs specify an exact application
commit. In-repository asset manifests use the commit containing the discovered
manifest and assets as their reference. All selected source and pack revisions
are recorded, even when discovery began at a mutable branch.

**`--latest --adapt` is an explicit authoring handoff.** It checks out the
application's current default-branch commit, applies changes only where their
original-file preconditions still hold, and preserves conflicting upstream files.
Files removed upstream remain absent when they conflict with the pack. Structural
conflicts involving symlinks, submodules, or file/directory changes fail rather
than guessing a merge. Even a conflict-free handoff is not model validation.

The checkout contains:

```text
.radpack/install.json             resolved revisions, provenance, status, hashes
.radpack/pack/                    exact portable pack snapshot
.radpack/reference/               reference model and portable assets (latest mode)
.radpack/upstream-changes.json    paths changed between source revisions (latest mode)
.radpack/adaptation.json          conflicts and adaptation context (latest mode)
.radpack/ADAPT.md                  Radius authoring instructions (latest mode)
```

Open the checkout in Copilot with the Radius tools and ask the agent to follow
`.radpack/ADAPT.md`. Review the existing model, upstream changes, and conflicts;
adapt through the Radius tools, validate the model, and inspect its graph.
The handoff explicitly calls out image build sources: source adaptations must
reach the build, and external dependency SHAs must not be blindly rewritten.
No automatic agent launch, model compilation, or cloud deployment occurs.

The receipt stays **`adaptation-required`** until a separate reviewed authoring
process is completed; Radpack has no command to certify or promote it to
model-validated. `verify` checks the initial handoff snapshot, not an agent's work,
and correctly detects edits made during adaptation. Export the reviewed result
as a new pack with `create`; never mutate the original published pack.

## Author a pack

The current CLI requires an explicit `--base`; automatic baseline selection and
an `init` command are not implemented. You can package an already-prepared
checkout: choose a commit available from the intended upstream repository.
If that commit already contains the integration, its selected files are retained.
Otherwise, required differences from that baseline become reviewed file operations.

1. Clone an application and record `git rev-parse HEAD` **before preparing it**.
   That full commit SHA is the upstream baseline.
2. Use Radius Canvas and the Radius tools to model the application, adapt it where
   necessary, and verify deployment in your own environment.
3. Preview a pack. Explicitly select application changes outside the default
   Radius assets; `--include` accepts individual repository-relative files and can
   be repeated. An included deleted file becomes a deletion in the pack.

```bash
radpack create \
  --source ../prepared-app \
  --base <full-upstream-commit-sha> \
  --output ./contrib/my-app \
  --include src/frontend/nginx.conf \
  --radius-version 0.60.0 \
  --dry-run
```

The release above is an example, not a recommendation: supply the Radius release
you actually used. This optional annotation does not install or validate Radius.
The compiler extension pin stays in the included `bicepconfig.json`.

The preview lists selected operations, excluded artifacts, unselected changes,
and blocking findings. Review both the inventory **and the selected file
contents**. If a necessary adaptation appears under "Not selected", add another
`--include` and preview again.

4. Repeat the command without `--dry-run`, adding `--approve <review-sha>` with the
   exact SHA printed by the preview. No output is written without this approval.
   A changed inventory invalidates the SHA. The destination must not exist.

```bash
radpack create \
  --source ../prepared-app \
  --base <full-upstream-commit-sha> \
  --output ./contrib/my-app \
  --include src/frontend/nginx.conf \
  --radius-version 0.60.0 \
  --approve <review-sha>
```

If `--output` is omitted, it defaults to `./contrib/<pack-name>` relative to your
current directory. The parent directories are created after approval. `--name`
overrides the repository-derived name. `--upstream owner/repo` overrides the
checkout's origin, for example when preparing in a fork; the baseline must be
fetchable from that upstream.

Create reads the working tree, including committed preparation changes relative
to the baseline. It never resets, stages, or commits the author's checkout.

## Inspect, install, and verify

```bash
radpack inspect ./contrib/my-app
radpack install ./contrib/my-app --directory ../my-app-test
radpack verify ./contrib/my-app --directory ../my-app-test
# Or verify against the exact pack snapshot saved during installation:
radpack verify --directory ../my-app-test
```

Installation fetches the pinned upstream commit, creates a local `radpack` branch,
checks original-file hashes, and applies the declared changes. The upstream
`origin` is preserved. Adaptations remain uncommitted so they are visible in Git.
An installation receipt and pack snapshot are written beneath `.radpack/`.
In latest mode the receipt records the selected revision separately from the
reference revision and never claims deployment verification.

Install validates the resolved pack before creating the destination and
automatically verifies the result. Discovery may fetch temporary repositories
first. An existing destination is never reused, even if empty. An ordinary
failure removes only the destination this invocation created; an interrupted
process may leave a partial directory that you must inspect before removing.

Verification checks the pinned HEAD and origin, receipt, declared file bytes and
modes, saved metadata, and absence of undeclared changes or extra
files (including ignored files). Run it **before building or deploying**, since
those operations legitimately introduce artifacts. It is a round-trip check, not
a general health check of an actively developed checkout. Matching installs also
check discovered local dependencies. Latest-mode handoffs may have missing or
changed dependencies to resolve during adaptation, so they do not claim that check.

Next, open the installed checkout in Radius Canvas to inspect the model. Create
**your own Radius environment** and supply your own deployment inputs through the
Radius tools. For GitHub Actions deployment, use a repository you control and
commit/push the prepared application there; installation does not fork, push, or
grant access to the upstream repository. Verify installation before changing its
origin or committing, as those intentionally change the baseline.

## What crosses the packaging boundary?

| Included or explicitly selectable | Excluded |
| --- | --- |
| `.radius/app.bicep` (or root `app.bicep`) | Generated `radius-environment.bicep` |
| Nearest `bicepconfig.json` and discovered local dependencies | Generated `radius-recipe-pack.bicep` |
| Existing `app.origin.json`, without rewriting its provenance | Generated `app-graph.json`, state, logs, and staging files |
| `custom-types.yaml`, `custom-recipe-pack.bicep`, referenced custom-type `.tgz` | Credential stores, kubeconfigs, `.rad/`, `.aws/`, `.azure/` |
| Explicitly included application changes, skills, instructions, reusable recipes | `.env` values, deployment parameter files, private keys |
| Secret declarations that take consumer-supplied inputs | Secret values and literal environment bindings |
| Explicit deletions of ordinary application files | `.git/`, installation metadata under `.radpack/`, and GitHub workflow changes |

Referenced local Bicep modules, imports, data files, and compiler extension paths
are added to the review. Referenced files must stay inside the source repository.
Unknown `.radius/` files are **not** automatically included. GitHub workflow
changes are excluded; let the consumer's Radius tooling generate them.

## Safety and limitations

- **Best-effort detection, not a no-secrets guarantee.** Filename rules and content
  checks reject common credentials, literal Radius Secret values, Kubernetes
  Secret data, cloud resource bindings, and environment definitions. They cannot
  identify every secret, encoding, or custom configuration. Findings report
  categories and paths, never matched values. Inspect selected contents and use
  your organization's secret scanning before distributing a pack.
- Checks run on export and import. There is no `--force` bypass for policy failures.
  A false positive must be resolved by making the asset portable or changing the
  policy with regression coverage, not by suppressing the check.
- UTF-8 files and inspected `.tgz` archives are supported, up to 8 MiB per payload,
  32 MiB combined, and 1,000 declared files. Archives are inspected without
  extraction: at most 32 MiB expanded, 1,000 entries, and UTF-8 regular files and
  directories only. Links, nested binary archives, credential files, and detected
  secret values are rejected. Other binary assets are not yet supported.
- Dependency discovery covers literal Bicep module/import/load paths and local
  compiler extension paths, not a full Bicep compiler or arbitrary application
  dependency analysis. Explicitly include other required files and verify with
  Radius. Remote modules, images, and recipes are not downloaded or vendored;
  authors must use immutable, consumer-accessible references.
- A local overlay does not change a source-build URL. If `app.bicep` builds from a
  Git URL, ensure that URL actually contains any required source adaptations.
  Installing an adaptation locally does not make it available to a remote builder.
- Existing origin records are preserved, not reissued as proof of compilation.
  Canvas may report a freshness mismatch when the recorded author commit differs
  from the installed upstream baseline. Radpack does not forge a fresh record.
- Checksums detect corruption and bind review to content; they are **not signatures**
  and do not establish publisher trust. Treat packs, application source, Bicep,
  recipes, and agent instructions as code. Only use trusted publishers.
- No pack hooks or repository Git hooks run during installation. Git still uses
  the operator's configured transport/authentication helpers and filters. No
  submodules or Git LFS objects are fetched. A payload affected by a checkout
  filter is rejected rather than silently producing different bytes.
- The upstream repository is cloned, not sanitized. Radpack's checks cover the
  declared portable assets and refuse known committed Radius runtime/environment
  artifacts, including generated `.radius/radius-environment.bicep`,
  `.radius/radius-recipe-pack.bicep`, credential stores, and kubeconfigs. They do
  not guarantee that arbitrary upstream source or Git history is free of secrets.
- `verify` does **not** compile Bicep, establish deployment readiness, or certify
  Azure/AWS compatibility. On Windows it checks content but not Unix executable
  bits. An author must test each provider independently in a fresh consumer
  environment; no provider verification claim is inferred from installation.

## Command reference

| Command | Purpose | Important options |
| --- | --- | --- |
| `create` | Preview or export a prepared application | Required `--base`; optional `--source`, `--output`, `--upstream`, `--name`, repeated `--include`, `--radius-version`; `--dry-run` or `--approve` |
| `inspect <pack-directory>` | Validate a portable pack and display its manifest | Operates on a local external pack or a saved `.radpack/pack/` snapshot |
| `install <target>` | Discover and install an application's integration | Optional `--directory`, `--pack` or `--pack-path`; paired `--latest --adapt` |
| `verify [pack-directory]` | Check an installation or initial adaptation snapshot | Required `--directory`; omitted pack uses the saved snapshot |
| `--help` | Show CLI usage | No repository or cloud access |

`create` without `--approve` only previews; `--dry-run` makes that intent explicit.
Errors return a nonzero exit code. A successfully prepared latest-source handoff
returns zero but explicitly reports `adaptation-required`, not deployment readiness.

## Troubleshooting

| Message or situation | What to do |
| --- | --- |
| No Radpack found | Supply an existing portable pack with `--pack`, or add an in-repository descriptor. The initial catalog has no application entries. |
| Destination already exists | Choose another directory. Radpack does not merge into or delete an existing checkout. |
| Approval does not match | Repeat the preview with the same intended options and review the new inventory before approving it. |
| Export blocked by a credential/environment finding | Replace concrete values with consumer-supplied inputs or remove the non-portable asset from the pack. Do not suppress the check or copy credentials into another file. |
| Git fetch failed | Check repository access and whether the recorded commit exists in that repository. Raw Git output is withheld because helpers and URLs can expose credentials. |
| Latest mode reports conflicts | Follow `.radpack/ADAPT.md` in a Radius-enabled authoring session. Upstream files were preserved; reference assets are available for comparison. |
| Verification fails after editing, building, or deploying | Verification compares the initial installation snapshot. Intentional edits and generated files change it; this does not by itself mean the application is broken. |
| Canvas reports a stale model | Review it using Radius's model validation/refresh workflow. Radpack preserves origin evidence rather than fabricating a fresh record. |
| Deployment builds old or unmodified code | Inspect application-owned Git build URLs. They must reference a published revision containing the source and required adaptations you intend to deploy. |

## Development and repository layout

```bash
go fmt ./...
go vet ./...
go test ./...
go test -race ./...
go build -o ./bin/radpack ./cmd/radpack
```

Tests create temporary local Git repositories and redirect a fixture GitHub URL
to them. They exercise the real Git fetch/checkout path without network access or
cloud credentials. CI runs the suite on Linux, macOS, and Windows.

```text
cmd/radpack/main.go             Native CLI entry point and cancellation
internal/radpack/cli.go         Command parsing and user-facing output
internal/radpack/discovery.go   Application, override, and catalog discovery
internal/radpack/pack.go        Pack validation and reviewed export
internal/radpack/install.go     Installation, verification, adaptation handoffs
internal/radpack/types.go       Typed wire contracts and strict JSON parsing
internal/radpack/policy.go      Path rules, content checks, archive inspection
internal/radpack/git.go         Git operations and remote revision resolution
internal/radpack/*_test.go      Native CLI, local Git, and policy regressions
catalog.go / catalog.json       Catalog embedded into the native executable
docs/pack-format.md             Manifest and receipt contracts
```

Go's `archive/tar` and `compress/gzip` inspect custom-type packages without
extracting them. There are no third-party Go dependencies or runtime package
installation steps. The catalog is embedded at build time, so moving the
executable does not break discovery.

The CLI still uses the installed Git executable and Radius remains a separate
tool. Sharing Go with Radius enables future integration; this migration does not
import Radius internals or invoke Radius during installation.

## Next milestone

The next product milestone is one real, manually prepared application pack:
capture it, install it into a fresh checkout, inspect its graph, and deploy using
a separate consumer-owned environment. Verify Azure and AWS independently.
The synthetic test applications are not runnable Radius samples or cloud
compatibility evidence.

Automatic adaptation, pack signing, automatic authoring-baseline discovery,
general binary assets, submodule/LFS fetching, and a Canvas install button are
not part of this initial implementation.

See [the format reference](docs/pack-format.md) for the manifest and
[contribution guidance](CONTRIBUTING.md) for the manual verification process.

## License

Radpack is [MIT licensed](LICENSE). Applications and recipes distributed through
packs retain their own licenses; pack authors must preserve required notices.
