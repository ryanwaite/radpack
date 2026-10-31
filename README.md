# Radpack

**Set up cloud-native applications, explore their architecture, and deploy to
your cloud of choice with Radius.**

## Try an application

For an application with a published Radpack:

```bash
radpack install owner/application
```

Open the installed checkout in the GitHub Copilot App with Radius Canvas.
Explore the application graph, create your own Radius environment, and deploy
through the Radius tools.

Radpack finds the integration and installs the application revision it was
prepared for. You supply your own environment and credentials.

## Share an application

Already prepared an application with Radius? Run this from its checkout:

```bash
radpack create
```

Radpack proposes the source revision, detects the Radius version, and shows what
the pack will contain. Select any additional application changes, then confirm:

```text
Create this pack? [y/N]
```

No initialization step or advance recordkeeping is required. Radpack saves the
pack under `contrib/<application-name>` by default. Creating a pack does not
certify that it has been deployment-tested.

**Early development:** the bundled catalog is currently empty. You can install
a local pack or point to a separately hosted pack. `owner/application` is a
placeholder for an application with a published integration, not an available
sample.

New here? [Install the CLI](#install-the-radpack-cli), then
[try a local pack](#try-your-first-local-pack).
For additional options, see [pack creation](#create-options-and-review),
[installation](#install-options-and-discovery), or the [command reference](#command-reference).

## Install the Radpack CLI

You need Go 1.24 or newer to build Radpack, and Git to use it.

```bash
git clone https://github.com/ryanwaite/radpack.git
cd radpack
go install ./cmd/radpack
radpack --help
```

`go install` places the executable in `GOBIN`, or `GOPATH/bin` when `GOBIN` is
unset. The usual location is `$HOME/go/bin`; add it to your PATH if necessary.
The installed executable does not need Go, Node.js, or runtime package
installation. Rerun `go install ./cmd/radpack` after changing the source.

To build without installing:

```bash
go build -o ./bin/radpack ./cmd/radpack
./bin/radpack --help
```

On Windows, use `go build -o .\bin\radpack.exe .\cmd\radpack`.
Prebuilt release binaries are not yet published.

Radius and its Canvas tools are separate prerequisites for modeling,
visualization, and deployment. Radpack does not install them.

## Try your first local pack

Start with an application checkout that already contains a Radius model and
has access to its source repository. With Radpack installed, run:

```bash
cd /path/to/prepared-app
radpack create --output ../my-app-pack
```

Review the proposed files and source revision, select required application
changes, and answer yes to save. Both commands below use the pack you just
created, so they do not need a catalog entry:

```bash
radpack install ../my-app-pack --directory ../my-app-test
radpack verify --directory ../my-app-test
```

Choose new paths for `my-app-pack` and `my-app-test`; Radpack does not overwrite
existing directories. Installation needs access to the recorded source
repository. In a script or redirected terminal, use the
[preview and approval flow](#preview-and-automation) instead of confirmation.

Open `my-app-test` in the GitHub Copilot App with Radius Canvas. You now have a
fresh application checkout with its Radius integration. Set up your own Radius
environment before deploying. `verify` checks the installed files, not whether
the application runs successfully in a cloud.

## How Radpack works

A pack combines an application's source revision with its reusable Radius
integration: the application model, recipes, and reviewed source adaptations.
It can live separately from the application or point to assets already committed
in the application's repository.

Radpack installs those files and records exactly which revisions it used.
Radius renders the graph, configures the consumer's environment, and deploys
the application. The author's credentials, cluster settings, and deployment
state stay out of the pack.

## Create options and review

Prepare the application however you normally work. Generate and inspect its
Radius model, make the required adaptations, and test deployment in your own
environment. From that application's checkout, run:

```bash
radpack create
```

There is no `init` prerequisite and no need to reconstruct which commits
originally added Radius.

Radpack finds the application model and proposes a published source revision.
It shows what consumers will receive: files retained from upstream, additions,
replacements, and deletions. The review also shows excluded files, Radius version
evidence, and warnings about image build sources.

Application changes outside the selected Radius assets stay excluded by default.
In a terminal, you can select individual files by number before the final
confirmation:

```text
Create this pack? [y/N]
```

Enter, no, or end-of-input cancels. After yes, Radpack rereads the files and review
evidence. If anything affecting the review changed, it stops without writing a
pack. Review the selected file contents as well as the inventory.

The output defaults to `./contrib/<pack-name>` relative to your current directory.
Choose another location when preparing an app outside the Radpack repository:

```bash
radpack create --output ../radpack/contrib/my-app
```

The destination must not exist. Radpack creates missing parent directories only
when saving the approved pack.

### How Radpack chooses the source

| Checkout situation | Create behavior |
| --- | --- |
| One source repository; current commit is fetchable from it | Use that exact commit. Already-published Radius files are retained rather than duplicated. |
| Uncommitted changes | Review them against the published baseline. Select additional application changes explicitly. |
| Current commit cannot be confirmed published | In a terminal, propose the remote's actual default-branch commit and ask before using it. |
| Multiple repositories, such as a fork and its upstream | Ask which repository consumers should clone. |
| Explicit `--upstream` or `--base` | Use the requested repository or full commit SHA after verifying it is fetchable. |

Source verification uses a temporary Git database. It does not change the
author's refs, index, or working files. Network access and normal Git
authentication are required. Radpack does not assume the default branch is
called `main`, and it never silently treats every feature-branch change as part
of the pack.

### Preview and automation

When either input or output is redirected, Radpack does not prompt or save
without explicit approval. Piping `yes` cannot approve an export.
`--dry-run` and `--approve` also disable prompts in a terminal.

```bash
radpack create \
  --source ../prepared-app \
  --output ./contrib/my-app \
  --include src/frontend/nginx.conf \
  --dry-run
```

Review the inventory and contents, then repeat with the printed review SHA:

```bash
radpack create \
  --source ../prepared-app \
  --output ./contrib/my-app \
  --include src/frontend/nginx.conf \
  --approve <review-sha>
```

`--include` accepts one repository-relative file and can be repeated. Selecting
a deleted file records a deletion. When source selection is ambiguous in
noninteractive mode, supply `--upstream owner/repo` and/or
`--base <full-published-commit-sha>`. `--name` overrides the repository-derived
pack name.

### Radius version detection

You normally do not need `--radius-version`. Create reads the nearest
`bicepconfig.json` and looks for an exact release in `extensions.radius`.
If that does not establish a release, a sibling `app.origin.json` can supply one
when its `appBicepHash` matches the current model bytes.

The compiler pin takes precedence. A stale origin record, conflicting versions,
or a mutable reference such as `latest` produces an explanation in the review.
If neither source establishes an exact release, `radiusVersion` remains `null`.
Registry digests and local extension packages do not establish release numbers
on their own.

`--radius-version <release>` remains an explicit override. Radpack labels it as
author-supplied and warns if it contradicts detected evidence. It does not change
the model or compiler configuration. Creation never runs Radius or reads a
deployment environment.

Version evidence participates in the review hash. A matching origin hash
describes an existing validation record; it is not a new compile or proof of
cloud compatibility.

### What goes into a pack

| Portable assets | Material that stays out |
| --- | --- |
| `.radius/app.bicep`, or root `app.bicep` | Concrete environment definitions and generated provider-pack copies |
| Compiler configuration and required local modules | Credential stores, kubeconfigs, private keys, and deployment parameter values |
| Custom resource types, reusable recipes, and inspected custom-type archives | Deployment state, cached graphs, logs, and staging output |
| Existing model-origin record | Invented or rewritten validation evidence |
| Explicitly selected application adaptations, skills, and instructions | Unselected application changes and GitHub workflow changes |
| Secret declarations with consumer-supplied inputs | The author's secret values |

Create discovers literal local Bicep imports, modules, loaded files, and compiler
extension paths. Unknown `.radius/` files are not included automatically.
Required files must remain within the repository.

Radpack checks the chosen source revision as well as the selected assets for
known Radius environment artifacts. Excluding a file from the overlay is not
enough if cloning the source would restore it.

## Install options and discovery

Name the application you want:

```bash
radpack install owner/application
```

Radpack finds its integration, clones the recorded application revision, and
applies the declared changes. The destination defaults to
`./<application-repository-name>`; change it with `--directory`.
An existing destination is never reused, even if empty.

Discovery follows this order:

1. An explicit `--pack` or `--pack-path` override.
2. `.radpack/radpack.json` in the application's current remote default-branch commit.
3. An immutable pack reference in the CLI's bundled `catalog.json`.

The catalog is embedded in the executable, not downloaded from a mutable remote
location. A broken in-repository manifest causes an error; Radpack does not
silently switch publishers.

### Choose the pack location

```bash
# External pack on disk
radpack install owner/application --pack ./contrib/my-app

# External pack in a GitHub repository
radpack install owner/application \
  --pack https://github.com/example/packs/tree/v1/contrib/my-app

# Manifest in another directory inside the application repository
radpack install owner/application --pack-path integrations/radius

# Install a local pack directly
radpack install ./contrib/my-app --directory ../my-app-test
```

`--pack` and `--pack-path` are mutually exclusive. External packs must identify
the requested application repository. GitHub pack URLs accept a single-segment
branch or tag, or an exact commit SHA. Use a SHA when a branch name contains `/`.
Catalog entries must use exact commit SHAs. Arbitrary download URLs are not
supported.

An application target can be `owner/repo` or a credential-free GitHub repository
URL. Existing local directories take precedence over similarly spelled repository
identifiers; use `./` to make local paths clear.

### Keep the integration upstream

When the application already contains its Radius assets, add a descriptor at
`.radpack/radpack.json`:

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

Paths are relative to the application repository root. Add required recipes and
other portable assets to the list. With `radiusVersion: null`, Radpack infers the
release from the application files.

The descriptor points to existing files; maintainers do not keep a second copy
in an overlay. It also does not try to name its own commit. Discovery resolves
the commit containing the descriptor and records that exact revision.

### Matching revision or latest source

By default, an external pack installs its recorded reference application commit.
An in-repository descriptor uses the commit containing the discovered descriptor
and assets. Radpack records the resolved source and pack revisions in both cases.

To bring an existing integration forward:

```bash
radpack install owner/application --latest --adapt
```

This checks out the application's current default-branch commit. Radpack applies
changes only where their original-file preconditions still hold. Conflicting
upstream files stay untouched; upstream deletions remain absent. Structural
conflicts involving symlinks, submodules, or file/directory changes cause an error.

The result is an **adaptation-required** checkout, not an automatically repaired
application. Radpack preserves the reference assets and writes instructions for
a human-guided Radius session:

```text
.radpack/install.json            Resolved revisions, provenance, status, hashes
.radpack/pack/                   Exact portable pack snapshot
.radpack/reference/              Reference assets for adaptation
.radpack/upstream-changes.json   Changed source paths
.radpack/adaptation.json         Conflict and adaptation context
.radpack/ADAPT.md                 Instructions for the Radius authoring session
```

The last four entries belong to latest mode. Open that checkout in Copilot with
the Radius tools and follow `.radpack/ADAPT.md`. Review changes, validate the
model, and inspect its graph. Radpack does not launch an agent, compile the
model, publish changes, or deploy.

The receipt remains `adaptation-required`; there is no status-promotion command.
After reviewing and verifying the adapted application, export a new pack rather
than modifying the published original.

### Version warnings do not block installation

After installation, Radpack compares the pack's recorded release with the local
Radius CLI using `rad version --cli --output json`. It uses `RADIUS_RAD_BINARY`
when configured, otherwise `rad` on PATH. The read-only query has a five-second
timeout and does not contact the control plane.

A different release, a missing CLI, an edge build without an exact release, or
a failed query produces a warning. Installation still succeeds. `0.60` and
`0.60.0` compare equal; prerelease differences are reported. Radpack also warns
when the installed model's evidence conflicts with the pack annotation.

The local CLI may differ from Canvas or the deployment workflow. Check the
Radius tooling you will deploy with. Radpack never upgrades Radius or treats a
version match as deployment verification. Integrity and portability failures
still block installation.

## Inspect and verify

```bash
radpack inspect ./contrib/my-app
radpack verify --directory ../my-app-test
```

`inspect` validates a portable pack and prints its manifest and checksum.
`verify` checks the saved pack, application revision, origin, receipt, file
contents and modes, and absence of undeclared changes or extra files, including
ignored files. Matching installations also check discovered local dependencies.
Latest-mode handoffs may have dependencies still to resolve during adaptation.

Installation performs this check automatically. You can supply an external pack
to compare against it:

```bash
radpack verify ./contrib/my-app --directory ../my-app-test
```

Run verification before editing, building, or deploying. It checks the initial
installation snapshot, not the health of an actively developed application.
Intentional edits, generated files, a new commit, or a changed origin can make
that comparison fail.

The commands do not establish that the model compiles or that the application
works on Azure or AWS. Verify deployment separately for each provider.

## From installation to deployment

Open the installed checkout in Radius Canvas and inspect its graph. Create your
own Radius environment and supply your own credentials and application secrets
through the Radius tools.

For GitHub Actions deployment, use a repository you control and commit and push
the prepared application there. Radpack leaves the upstream origin in place and
application adaptations uncommitted for inspection; it does not fork, push, or
grant access to the original repository.

**Make sure the build uses the application you installed.** A Bicep model can
tell an image builder to fetch a different Git commit. Local file changes do not
reach that remote builder just because they exist in the installed checkout.
If the pack adapts application source, the build must use a published revision
containing those adaptations, or a supported local build path.

Create warns about literal Git build sources that name another revision or
repository, use mutable references, or omit local changes inside a build context.
It does not rewrite those references. Dynamic expressions and application-specific
build behavior still need review.

## Safety and current limits

- **Secret detection is best-effort.** Path rules and content checks catch common
  credentials and environment bindings, not every secret or encoding. Review
  selected contents and use your organization's secret scanner. There is no
  `--force` bypass for policy failures.
- **Checksums are not signatures.** They detect changes and bind approval to
  reviewed content; they do not establish publisher trust. Models, recipes,
  application code, and agent instructions must come from publishers you trust.
- **Payloads are bounded.** Radpack supports UTF-8 files and inspected `.tgz`
  archives, up to 8 MiB per payload, 32 MiB combined, and 1,000 declared files.
  Archives permit at most 32 MiB expanded content and 1,000 entries. Members must
  be UTF-8 regular files or directories; links and other binary content are refused.
- **Discovery is not a compiler.** Local dependency checks cover literal Bicep
  references and compiler extension paths. Remote modules, recipes, and images
  are not downloaded or vendored. Authors must make them accessible to consumers.
- **Git remains a dependency.** Radpack disables repository hooks but uses the
  operator's configured transport/authentication helpers and filters. It does not
  fetch submodules or Git LFS objects. Selected files changed by checkout filters
  are rejected instead of silently installing different bytes.
- **Upstream is not sanitized.** Known committed Radius environment artifacts
  are refused. This does not guarantee that arbitrary upstream files or Git
  history contain no secrets.
- **Origin evidence is preserved.** Radpack does not invent a fresh validation
  record. Canvas may request review when the model's recorded source differs
  from the installed baseline.
- **Failed installs do not overwrite existing checkouts.** Ordinary failures
  remove only the destination created by that invocation. An interrupted process
  can leave a partial directory; inspect it before removing it.

## Command reference

| Command | Purpose | Options |
| --- | --- | --- |
| `create` | Review and save a prepared application's pack | `--source`, `--output`, `--upstream`, `--base`, `--name`, repeated `--include`, `--radius-version`, `--dry-run`, `--approve` |
| `inspect <pack-directory>` | Validate and display a portable pack | Local pack directory |
| `install <target>` | Discover and install an integration | `--directory`, `--pack` or `--pack-path`, paired `--latest --adapt` |
| `verify [pack-directory]` | Compare an installation with its recorded snapshot | Required `--directory` |
| `--help` | Show CLI usage | No repository access |

Errors return a nonzero exit code. Cancelling an interactive export writes no
pack. A completed latest-source handoff returns zero with
`adaptation-required`; that status does not mean the model is ready to deploy.

## Troubleshooting

| Situation | Next action |
| --- | --- |
| No Radpack found | Supply `--pack` or add an in-repository descriptor. The bundled catalog is empty. |
| Source repository or baseline is ambiguous | Choose interactively, or pass `--upstream` and/or `--base`. |
| Source commit cannot be fetched | Check access and whether that commit is available from the intended repository. Raw Git diagnostics are withheld to avoid leaking credentials. |
| Checkout changed during review or approval no longer matches | Run create again and review the new contents. |
| Destination already exists | Choose a new directory. Radpack will not merge into the existing one. |
| Credential or environment policy blocks export | Remove concrete values or choose a clean source revision. Do not move credentials elsewhere to bypass a check. |
| Radius versions differ | Installation can continue; validate with the Radius tooling you intend to use. |
| Latest mode reports conflicts | Follow `.radpack/ADAPT.md` and compare preserved upstream files with the reference assets. |
| Canvas reports a stale model | Review it through Radius's validation/refresh workflow. |
| Deployment builds old code | Check application-owned Git build URLs and whether the published build source contains required adaptations. |

## Development

```bash
go fmt ./...
go vet ./...
go test ./...
go test -race ./...
go build -o ./bin/radpack ./cmd/radpack
```

Tests use temporary local Git repositories, simulated review input, and helper
executables for version queries. They do not need cloud credentials or a running
Radius environment. CI covers Linux, macOS, and Windows. Unix executable-bit
verification is skipped on Windows.

| Location | Responsibility |
| --- | --- |
| `cmd/radpack/main.go` | Native entry point and cancellation |
| `internal/radpack/cli.go` | Command parsing |
| `internal/radpack/create_source.go` | Published-source inference and verification |
| `internal/radpack/create_review.go` | Terminal review, file selection, confirmation |
| `internal/radpack/build_sources.go` | Source-state checks and build warnings |
| `internal/radpack/discovery.go` | Application, override, and catalog discovery |
| `internal/radpack/pack.go` | Pack validation and export |
| `internal/radpack/install.go` | Installation, receipts, verification, adaptation handoffs |
| `internal/radpack/policy.go` | Paths, content checks, archive inspection, local dependencies |
| `internal/radpack/radius_version.go` | Version evidence and local CLI warnings |
| `internal/radpack/types.go` | Typed records and strict JSON parsing |
| `internal/radpack/git.go` | Git operations |
| `catalog.go`, `catalog.json` | Catalog embedded at build time |

Go's standard archive readers inspect custom-type packages without extracting
them. The pinned `golang.org/x/term` dependency and its `x/sys` dependency handle
terminal detection. Sharing Go with Radius does not mean Radpack imports Radius
internals or performs deployments.

See the [format reference](docs/pack-format.md) for manifests and receipts, and
the [contribution guide](CONTRIBUTING.md) for preparing and verifying a pack.

The next product milestone is a real application pack installed into a fresh
checkout, inspected in Canvas, and deployed in a separate consumer environment.
Automatic adaptation, pack signing, general binary assets, and a Canvas install
button are not implemented.

## License

Radpack uses the [MIT license](LICENSE). Applications and recipes retain their
own licenses; pack authors must preserve required notices.
