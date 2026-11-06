# Radpack

**Set up cloud-native applications, explore their architecture, and deploy to
your cloud of choice with Radius.**

## Try an application

For an application with a published Radpack:

```bash
radpack install owner/application
```

Radpack opens a session draft for the clean source clone. Start a session at the
recorded revision; the updated Radius extension offers to apply the integration
to the app-created worktree. Approve that review to see the existing graph.
Then create your own Radius environment and deploy through the Radius tools.

Radpack finds the integration and installs the application revision it was
prepared for. You supply your own environment and credentials.

## Share an application

Start from an application on GitHub:

```bash
cd ~/Source
radpack create https://github.com/Azure-Samples/aks-store-demo
```

Radpack creates `~/Source/aks-store-demo`, clones an exact upstream revision,
and records it before you make changes. It offers a **private independent
authoring repository**, such as `your-account/aks-store-demo-radpack`, using your
selected GitHub account. It asks before creating the repository, disabling its
Actions, and publishing the pinned source snapshot. No upstream contributor
permission or GitHub fork relationship is required. Declining continues with
local-only authoring. For a repository you administer, use `--use-source` instead.

Once that choice is settled, Radpack asks you to Radify the application with
the Radius tools and return to press Enter. You can close the terminal and
resume later:

```bash
radpack create --resume ~/Source/aks-store-demo
```

Already prepared an application independently? Run this from its checkout:

```bash
radpack create
```

Radpack proposes the source revision, detects the Radius version, and shows what
the pack will contain. Select any additional application changes, then confirm:

```text
Create this pack? [y/N]
```

No separate initialization command or advance recordkeeping is required. After
review and approval, Radpack saves a local pack directory. Saving does not
publish it or certify that it has been deployment-tested.

For an independently prepared checkout, the separate
[`radpack fork` command](#prepare-a-personal-fork-for-deployment) offers the same
deployment setup. In either flow, the original application identity stays
separate from the deployment repository.

The application can belong to someone else. Its upstream repository remains
the application's identity; the pack can be reviewed and published separately.
Saving a pack never pushes source code or creates a pull request.

**Early development:** the bundled catalog is currently empty. You can install
a local pack or point to a separately hosted pack. `owner/application` is a
placeholder for an application with a published integration, not an available
sample.

New here? [Install the CLI](#install-the-radpack-cli), then
[try a local pack](#try-your-first-local-pack).
For additional options, see [pack creation](#review-an-existing-prepared-checkout),
[installation](#install-options-and-discovery), or the [command reference](#command-reference).

## Install the Radpack CLI

You need Go 1.24 or newer to build Radpack, and Git to use it.
Independent repository and optional fork setup also require GitHub CLI (`gh`) authenticated as the intended
personal account.

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
The desktop handoff currently requires a local Radius development build containing
the Radpack handoff integration. Those companion changes have not yet been
published in the marketplace plugin; installing the marketplace version alone
does not enable the handoff. Until a supporting release is available, use that
updated local build or install packs directly with `--no-open`.
Radpack does not replace development extensions or install a duplicate plugin.

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
radpack install ../my-app-pack --directory ../my-app-test --no-open
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

## Start, pause, and resume authoring

`radpack create <GitHub URL>` accepts a repository URL or `owner/repo`. It creates
a new checkout beneath the current directory using the repository name, much
like installation. It does **not** look for an existing pack or require a model
to exist yet.

```bash
radpack create owner/application \
  --directory ./application-authoring \
  --output ./contrib/application
```

`--directory` chooses the new source checkout; `--output` chooses the eventual
pack directory. They are different things. Existing source directories are never
reused or overwritten. The default source revision is the remote default branch's
current commit, or an exact published SHA supplied with `--base`.

The authoring record saves the original repository and revision, absolute source
and output paths, and initial file/recipe selections. It lives inside local Git
metadata, not in the application's tracked files or a pack. It stores no cloud
credentials or Radius environment state.

### Choose the deployment repository before Radifying

URL-first creation defaults to a **private independent authoring repository**,
not a GitHub fork. Radpack needs read access to the original source and permission
to create a repository in the personal account selected by `gh`. It never calls
the upstream administrator or fork API for this path. It cannot bypass source
access restrictions, your account's repository-creation policies, or license
requirements.

The review shows the destination and visibility, the pinned original commit,
every file to be published, source-publication warnings, and affected worktrees:

```text
Create/use private repository your-account/application-radpack, disable Actions, and publish this source snapshot? [y/N]
```

This is a source-publication approval, separate from saving a pack. Answering
`y` authorizes the following:

1. Create a private repository, or use an explicitly selected empty repository
   that you own and administer. A matching existing name is never implicitly
   claimed. Nonempty destinations are refused, not overwritten.
2. Disable GitHub Actions and verify that policy before pushing. Upstream
   workflow files are preserved, but are not silently activated.
3. Publish exactly the pinned source tree as one new root commit on
   `radpack-authoring`. Original files, binary assets, and license/notice files
   are retained. The commit message records the original repository and commit.
   No upstream history, tags, other branches, or local uncommitted changes are
   published.
4. Set the new repository as `origin`, preserve the original as `upstream`, and
   switch this checkout to a new `radpack-authoring` branch tracking the published
   branch. The old local branch and uncommitted files are preserved. Other linked
   worktrees share the remote changes but are not switched.

Pressing Enter or answering `n` skips when no operation is pending. EOF and
cancellation leave the choice unresolved. JSON and redirected callers need
`--repository-approve <review-sha>`. A pending creation/publication cannot be
discarded with `--no-fork`; inspect and resume the recorded operation.

Actions stay disabled after setup. Reopen the same Radius instance explicitly
for the new repository and run its readiness checks. Enabling Actions and
granting cloud access are separate user decisions; Radpack does not do either.
Once you Radify the application, normal commits and pushes from
`radpack-authoring` update your independent repository without republishing
upstream history. The portable pack still compares your work with the original
upstream revision.

The snapshot inspector blocks known token/private-key signatures, unsupported
symlinks/submodules, Git LFS pointers, files over 8 MiB, and snapshots over
32 MiB. Other suspicious source content is listed for review without printing
values. Binary content is checked for known signatures, not semantically
inspected. These checks are best-effort, not proof that redistribution is safe
or licensed. If inspection blocks a repository, prepare it manually rather
than bypassing the review.

Credential-bearing URLs normally block publication. The literal tutorial pair
`username:password` on exactly `localhost`, `127.0.0.1`, or `::1` is instead a
review warning in text files. Other credentials or hosts still block, including
when they appear alongside an allowed example. This exception applies only to
initial source snapshots; portable pack credential checks remain unchanged.

```bash
# Default private destination: your-account/application-radpack
radpack create owner/application

# Choose a new name, or explicitly select an empty repository you own
radpack create owner/application --repository your-account/my-application

# Explicitly choose public visibility before approving publication
radpack create owner/application --public

# Restore a private choice before publication (does not change a live repository)
radpack create --resume ./application-authoring --private

# Choose local authoring without GitHub account or permission checks
radpack create owner/application --no-fork

# Explicitly request the personal-fork path, still requiring approval
radpack create owner/application --fork

# Use the original repository when you administer it
radpack create owner/application --use-source

# Choose a different personal-fork name; this also requests the fork path
radpack create owner/application --fork-name application-radius
```

`--no-fork` still needs source access to clone, but does not inspect GitHub account
permissions, create a repository, or publish source. The choice is remembered
on resume. Use `--independent` later to opt into the default repository path.

`--fork` retains the optional GitHub fork flow. Its interactive `[Y/n]` prompt
names the exact create/reuse action; Enter approves it, but EOF and cancellation
do not. That path still depends on the upstream fork API, so it can encounter
organization SSO restrictions. It never pushes local source. Noninteractive
fork consent remains `--fork-approve`, distinct from `--repository-approve`.

`--use-source` verifies that the selected account can administer the original
repository and that it is active. It remembers that choice and does not fall
back to a fork if access is missing. A failed check returns
`awaiting-github-access`; resolve access, choose `--independent`, or continue with
`--no-fork`. Source administration does not establish Actions policy, package
access, or cloud permissions; Radius checks those during deployment setup.

Previously recorded choices remain unchanged. An older workflow that recorded
the administrator-first `auto` or fork policy keeps it until you explicitly
select another mode. For a checkout paused before successful fork setup, switch
once without recloning:

```bash
radpack create --resume ./application-authoring --independent
```

An unresolved or completed fork binding cannot be silently replaced by an
independent publication. Resolve it first. Independent setup also requires the
checkout's committed tree to match the pinned source and the local
`radpack-authoring` branch not to exist; committed Radification work must be
preserved and handled deliberately, not overwritten.

After setup or a local-only choice, an interactive terminal displays:

```text
Go Radify your app. When finished, press Enter to review;
type q to pause (you can close this terminal and resume later):
```

Use the Radius tools in the checkout or an app-created linked worktree to prepare
the application. Radpack does not start modeling, create an environment, run
setup scripts, open the Copilot App, or deploy as part of this authoring step.
Enter means "inspect my work", not "declare it ready". If the model is still
absent, the workflow remains waiting and can be resumed again.

### Returning to your work

```bash
# Continue the recorded workflow
radpack create --resume ./application-authoring

# Read progress without reviewing or saving
radpack create --resume ./application-authoring --status --json

# Explicitly select a Copilot-created linked worktree
radpack create --resume ./application-authoring \
  --source /path/to/app-worktree
```

You can also pass the linked worktree directly to `--resume`. If several linked
checkouts exist and none was selected, the terminal asks which one contains the
Radius work. Noninteractive callers receive `awaiting-worktree-selection` with
the available paths; Radpack never silently exports the clean original clone.
Unrelated clones, even of the same GitHub repository, cannot be selected.

Resume preserves the recorded starting commit even if upstream advances or you
commit the Radius work locally. To deliberately choose another baseline, use
ordinary `create --source ... --base ...` outside this recorded workflow.
Output paths and initial `--include`, `--recipe`, and `--recipe-pack` selections
survive terminal restarts. Resume can add selections and override `--output`.
Repository mode, destination, and visibility are saved separately. A completed
repository is rechecked on normal resume rather than recreated or republished;
`--status` only reads local progress and does not make GitHub API calls.
When no completed repository or skip decision exists, read-only status reports
`awaiting-deployment-choice` instead of treating an old permission check as
current authorization.

After a successful save, resume verifies the saved pack and reports `saved`
without creating it again. Publication remains separate. A missing or modified
saved pack is a visible error, not permission to overwrite it. An interrupted
review can leave a local `review.lock`; inspect the active process and saved
output before removing the lock and resuming.

### Agent-friendly authoring

```bash
# Clone and return immediately; never wait for terminal input
radpack create owner/application --directory ./application-authoring --json

# Approve repository creation/use, disabling Actions, and snapshot publication
# described by repositorySetup.publicationReview
radpack create --resume ./application-authoring \
  --repository-approve <publication-review-sha> --json

# Resume after the agent or user prepares the model
radpack create --resume ./application-authoring --json

# Save the exact reviewed pack
radpack create --resume ./application-authoring --json --approve <review-sha>
```

Starting with a URL clones and records the workflow. JSON or redirected callers
receive `awaiting-repository-approval` when independent setup needs consent
(`awaiting-fork-approval` for the optional fork flow),
`awaiting-github-access` when a source-mode permission check is unavailable, or
`awaiting-radification` when the explicitly selected source is suitable or setup
was skipped. Repository API failures return `blocked` with a diagnostic.
No noninteractive invocation silently approves creation or publication. This is true even when
upstream already contains a Radius model. `--dry-run`, `--approve`, `--source`,
and `--upstream` cannot be combined with a starting URL; use them on the
appropriate resume or ordinary checkout flow. This keeps `--dry-run` from
unexpectedly cloning files and keeps pack-save approval separate from starting
an authoring checkout.

`--repository-approve` and `--fork-approve` are accepted only on resume and must
match their respective reviews. They cannot be combined with each other or
`--approve`, `--dry-run`, or `--status`. `--approve` authorizes only the pack save.
An agent must explain the destination, visibility, source publication, Actions
policy, and local branch/remote changes before obtaining the user's approval.
Interrupted operations keep their journal. A lost repository-creation response
without a confirmed repository ID requires manual inspection; a matching name
alone is not permission to claim it. A lost push response is reconciled against
the exact recorded remote commit; conflicting refs are never overwritten.

Workflow reports identify the source, baseline, selected worktree, output,
resume arguments, and `repositorySetup` review/status. They distinguish
`awaiting-deployment-choice`, `awaiting-github-access`, `awaiting-repository-approval`, `awaiting-fork-approval`,
`awaiting-radification`,
`awaiting-worktree-selection`, `ready-for-review` (status inspection only),
`saved`, and `blocked`. Once resume enters the actual review, it returns the
existing create report with `awaiting-review`, readiness findings, and approval
hash. `repositorySetup.sourcePublication` reports `not-performed`, `unconfirmed`,
or `confirmed` for initial snapshot publication. The top-level `publication`
still describes pack publication, not this source push, and remains
`not-performed`. Cloud credentials and deployment are not inspected or run.

## Review an existing prepared checkout

Prepare the application however you normally work. Generate and inspect its
Radius model, make the required adaptations, and test deployment in your own
environment. From that application's checkout, run:

```bash
radpack create
```

There is no `init` prerequisite and no need to reconstruct which commits
originally added Radius.

This existing-checkout path is still available independently of URL-first
authoring. It infers a published baseline as described below. To retain a
URL-started workflow's original baseline and progress, use `--resume` instead.

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

### Include reusable recipes and recipe packs

Declare the integration entry points explicitly:

```bash
radpack create \
  --recipe .radius/recipes/rabbitmq.bicep \
  --recipe-pack .radius/recipe-packs/azure.bicep \
  --recipe-pack .radius/recipe-packs/aws.bicep
```

Each flag accepts an individual file and can be repeated. Recipes may be Bicep,
Terraform `.tf`, or `.tf.json` files. Recipe-pack entry points must be Bicep
files declaring `Radius.Core/recipePacks` resources. Literal local Bicep module
dependencies are included in the review. Include other required scripts and
Terraform module files with `--include`; Radpack does not resolve a complete
Terraform module automatically.

The conventional `custom-recipe-pack.bicep` beside the application model is
recognized automatically. An authored file named `radius-recipe-pack.bicep`
can also be included with `--recipe-pack`: its name alone does not make it an
environment instance. Without an explicit recipe-pack role that generated
filename remains excluded. Declaring a role does not bypass content checks:
concrete environment resources, credential values, and forbidden directories
remain blocked.

Packs with recipe roles use **portable format v2**. Their manifest records which
files are recipes and recipe packs, alongside the existing file operations and
checksums. Packs without role declarations continue to use v1, and this CLI
reads both formats. Older CLI versions reject v2 rather than silently ignoring
the integration metadata.

These files are reusable definitions, not the author's deployment. Installation
preserves them but does not register them or attach them to an environment.
Use the Radius tools to register recipes, attach the appropriate packs, and
supply consumer-owned inputs when setting up deployment. A declared Azure or AWS
recipe pack does not itself establish that the provider has been tested.

Create discovers literal local Bicep imports, modules, loaded files, and compiler
extension paths. Unknown `.radius/` files are not included automatically.
Required files must remain within the repository.

Radpack checks the chosen source revision as well as the selected assets for
known Radius environment artifacts. Excluding a file from the overlay is not
enough if cloning the source would restore it.

### Machine-readable review for agents

Agents can preview and save without terminal questions:

```bash
radpack create --recipe-pack .radius/recipe-packs/azure.bicep --json
radpack create --recipe-pack .radius/recipe-packs/azure.bicep \
  --json --approve <review-sha>
```

`--json` disables prompts. Standard output contains one JSON report with the
current stage, review hash, selected assets, readiness findings, and next actions.
Status is `awaiting-review`, `saved`, or `blocked`; a cleanup error after a
successful save is reported as `saved-with-cleanup-error`. In a resumed authoring
workflow, a failed completion record is `saved-with-checkpoint-error`: the pack
exists and must not be blindly recreated. A blocked operation
returns nonzero without exposing matched secret values. Source ambiguity,
missing models, and policy failures have distinct diagnostic codes.

Agents must not infer that a Git access failure means credentials are missing.
The report identifies unconfirmed source access and asks for the needed
repository/revision decision without reading credential stores.

Save and publication are separate. This phase produces a local pack directory.
It does not introduce an archive format, publish recipes, upload application
source, or submit a catalog contribution automatically.

## Install options and discovery

Name the application you want:

```bash
radpack install owner/application
```

Radpack finds its integration, stages and verifies the assembled application,
clones the recorded source revision, and opens a Copilot App session draft.
The source clone stays clean. After you start an app-created worktree session
and approve the Radius handoff, the integration is applied there.
The destination defaults to
`./<application-repository-name>`; change it with `--directory`.
An existing destination is never reused, even if empty.

For a direct installation without the desktop handoff:

```bash
radpack install owner/application --no-open
```

This applies the pack immediately to the new checkout and does not launch the
app or write handoff state. It is the appropriate mode for automation and for
using a Radius extension that does not yet support the handoff.

Default desktop installation saves requests and claims under `~/.radpack/handoffs/`
and staged applications under `~/.radpack/candidates/`. Both locations are local
state outside the application repository.
The app session must use a clean linked worktree of the prepared clone at the
recorded commit. Existing setup-script output, user edits, or an unexpected
revision block application rather than being overwritten. The user still starts
the session and accepts repository trust and pack-application prompts.

If the app is missing or does not open, the clone and pending handoff remain:

```bash
radpack open --directory /path/to/prepared-source
```

This retries opening a session draft without recloning. Pending requests expire
after seven days; local state is retained for inspection. See the
[handoff protocol](docs/copilot-handoff.md) for its ownership and recovery rules.

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
radpack install ./contrib/my-app --directory ../my-app-test --no-open
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

Once applied to the app worktree (or installed directly with `--no-open`), the
result is an **adaptation-required** checkout, not an automatically repaired
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

After preparation or direct installation, Radpack compares the pack's recorded release with the local
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

Pack application performs this check automatically. For desktop handoffs, run
verification against the app worktree after approval, not the clean source clone.
You can supply an external pack
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

## Check build and recipe readiness

```bash
# Inspect the saved pack without fetching its retained upstream files
radpack report ./contrib/my-app

# Inspect all declared assets in a verified installed checkout
radpack report --directory ../my-app-test
```

Both commands return JSON. The report lists build-source findings, recipe
entry points, recipe-pack entry points, and machine-readable issues with a
`nextAction`. It distinguishes:

| Build status | Meaning |
| --- | --- |
| `reference-checks-passed` | The observed literal Git application references match the selected source and show no detected overlay gap. This is not a build or deployment test. |
| `requires-source-assembly` | The pack changes files within a Git build context. Fetching upstream alone will omit those changes. |
| `review-required` | A revision differs, a source is dynamic/external, or required contents are unavailable for inspection. |
| `not-assessed` | No literal Git build references were found; parameterized builds, local contexts, or prebuilt images need review. |

Recipe source references are assessed separately. Exact Git commits and OCI
digests identify immutable inputs; tags, dynamic sources, and unresolved
references require review. The report never checks registry access or downloads
those dependencies. Terraform module dependencies require explicit inspection.

Compilation and deployment verification remain `not-performed`, environment
recipe registration remains `not-checked`, and credentials remain
`not-inspected`. Missing retained files in a pack-only report are marked unknown;
use the installed-checkout form to inspect them. For desktop installs, point it
at the app worktree after applying the pack, not the clean source clone.

The current Radius Git image-build path does not assemble a Radpack overlay.
This phase reports that requirement explicitly; implementing upstream-plus-pack
builds is a separate step. A pack can be saved for further work without falsely
being labeled ready to deploy.

## From installation to deployment

### Prepare a personal fork for deployment

Cloning a public application does not grant permission to create GitHub
environments or workflows in its original repository. Radius must target a
repository you administer.

From the authoring checkout or an installed application, run:

```bash
radpack fork --directory ./aks-store-demo
```

Radpack uses the personal account identified by `gh` and proposes a fork under
that account. The default name matches the application; use `--name` to choose
another name when a repository already occupies it:

```bash
radpack fork --directory ./aks-store-demo --name aks-store-demo-radius
```

The review shows the original source and commit, proposed deployment fork,
whether it will create or reuse the fork, and all linked checkouts affected by
the remote change. Public source forks are public. Answering no makes no changes.
The command never silently reuses an unrelated repository or creates a fork in
an organization.

After approval, Radpack verifies the destination's personal owner, fork
relationship, administrator permission, public/active status, and access to the
recorded application commit. It then sets:

```text
origin   -> your personal fork, used by Radius for deployment
upstream -> the original application repository
```

Git remotes are shared across linked worktrees. Radpack does not change the
branch, working files, index, or existing commit history. It does not push your
local model/adaptations, create an environment, enable Actions, or provision
cloud resources. Existing explicit push URLs, mirror settings, multiple origin
URLs, and conflicting upstream remotes cause a refusal rather than being
silently replaced.

The original source repository and baseline remain in the pack and authoring
record. A Git-local deployment binding records the personal fork separately;
it is never exported in a pack. Create, resume, installation verification, and
desktop handoffs honor that binding. Manually changing `origin` without a
recorded binding still fails installation verification.

For automation:

```bash
radpack fork --directory ./aks-store-demo --dry-run --json
radpack fork --directory ./aks-store-demo --approve <review-sha> --json
```

Preview needs GitHub CLI authentication but makes no changes. Noninteractive
execution never implicitly approves. Radpack binds approval to the selected
account, destination, source, current remotes, and linked checkouts.
GitHub fork creation is asynchronous: if creation succeeds but the repository
or commit is not available yet, the report preserves the operation and asks you
to retry with the same approval. An ambiguous response is not permission to
send a second creation request. Inspect the personal repository if its outcome
remains unconfirmed.

The `gh` credential must be authorized for the requested operation. Forking can
still report SAML or access restrictions when GitHub enforces them; Radpack does
not bypass SSO or try another account. Once Radius targets your personal fork,
its environment setup should no longer administer the upstream organization.
An independent repository or an organization-owned deployment target is not
configured by this command.

### Select the fork in Radius

Fork setup does not rewrite a live Canvas selection. Reopen the **same Radius
canvas instance** on the environment page with the personal fork explicitly
selected (`page: environment`, `repo: your-account/your-fork`), then rerun its
GitHub account/readiness checks. An already-open panel does not watch Git remotes
for changes. Do not
continue an environment operation that was started against the original repo.
If the Copilot App still identifies the upstream repository, start a new session
for the fork and deliberately preserve or transfer the prepared changes.

The companion Radius change refreshes repository-context checks and checks
Actions enablement/policy separately from the token's workflow scope. It must
be loaded before relying on that improved readiness display. A token with
`workflow` scope alone is not proof that Actions is enabled or that its policy
permits the generated workflows.
For personal accounts, the current check verifies enabled Actions with
`allowed_actions: all`. Selected-action allowlists are reported as unverified;
it does not evaluate their transitive action dependencies. It never changes
policy or recommends weakening an organization's restrictions to make a check
pass.

Commit and push the prepared source to the fork only when you choose to publish
those files. Radius's GitHub workflows need the model on the branch they check
out. Fork setup itself publishes only GitHub's fork of existing upstream history,
not any uncommitted local application or Radius changes.

### Configure and deploy

Use the app worktree after approving the desktop handoff, or open the directly
installed checkout when using `--no-open`. Inspect its Radius graph. Create your
own Radius environment and supply your own credentials and application secrets
through the Radius tools.

For GitHub Actions deployment, use a repository you control and commit and push
the prepared application there. Radpack leaves the upstream origin in place and
application adaptations uncommitted for inspection; it does not fork, push, or
grant access to the original repository.
When using the recorded fork binding, Radpack verification accepts the personal
fork as `origin` while continuing to compare application files with the original
source baseline.

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
- **Failures preserve existing work.** A failed direct install cleans up only
  its newly created directory. A failed app launch preserves the clean source
  and pending handoff. A failed worktree application leaves its claim and files
  for inspection; it never removes the app worktree or blindly retries partial
  writes. An interrupted process can leave partial output.

## Command reference

| Command | Purpose | Options |
| --- | --- | --- |
| `create <GitHub URL>` | Clone, review a private snapshot repository, and pause for Radification | `--directory`, `--output`, `--repository`, `--public`, optional `--base`, asset selections, `--independent`, `--fork`, `--no-fork` or `--use-source`, `--json` |
| `create --resume <checkout>` | Continue a recorded authoring workflow | `--source <linked-worktree>`, `--status`, `--repository-approve` or `--fork-approve`, review/save options, `--json` |
| `create` | Review and save a prepared application's pack | `--source`, `--output`, `--upstream`, `--base`, `--name`, repeated `--include`, `--recipe`, `--recipe-pack`, `--radius-version`, `--dry-run`, `--approve` |
| `create --json` | Agent-readable preview or approved save | Same options; never prompts |
| `inspect <pack-directory>` | Validate and display a portable pack | Local pack directory |
| `report [pack-directory]` | Report build-source and recipe readiness as JSON | Supply a pack directory or `--directory <installed-checkout>` |
| `fork` | Review and create/reuse a personal deployment fork | `--directory`, `--name`, `--dry-run` or `--approve`, `--json` |
| `install <target>` | Prepare an app handoff, or install directly | `--directory`, `--pack` or `--pack-path`, paired `--latest --adapt`, `--no-open` |
| `open` | Retry opening a prepared clone in the Copilot App | Required `--directory`; optional operator environment `RADPACK_GITHUB_COMMAND` |
| `verify [pack-directory]` | Compare an installation with its recorded snapshot | Required `--directory` |
| `--help` | Show CLI usage | No repository access |

Errors return a nonzero exit code. Cancelling an interactive export writes no
pack. A desktop launch acknowledgment does not mean a session or graph is ready.
A completed direct latest-source install reports `adaptation-required`; that
status does not mean the model is ready to deploy.

## Troubleshooting

| Situation | Next action |
| --- | --- |
| No Radpack found | Supply `--pack` or add an in-repository descriptor. The bundled catalog is empty. |
| Source repository or baseline is ambiguous | Choose interactively, or pass `--upstream` and/or `--base`. |
| Source-mode or older URL-create cannot verify GitHub administration | Resume with `--independent` before a fork binding exists, fix selected `gh` access, or use `--no-fork` for local work. |
| Authoring has multiple worktrees | Resume with the worktree path or specify `--source`; choose the checkout containing the Radius edits. |
| Authoring review is active or interrupted | Check the process and output before removing its Git-local `review.lock`. Do not reset the prepared checkout. |
| Source commit cannot be fetched | Check access and whether that commit is available from the intended repository. Raw Git diagnostics are withheld to avoid leaking credentials. |
| Checkout changed during review or approval no longer matches | Run create again and review the new contents. |
| Destination already exists | Choose a new directory. Radpack will not merge into the existing one. |
| Credential or environment policy blocks export | Remove concrete values or choose a clean source revision. Do not move credentials elsewhere to bypass a check. |
| Radius versions differ | Installation can continue; validate with the Radius tooling you intend to use. |
| Radius checks the upstream repository instead of your fork | Explicitly reopen for the fork and rerun readiness. Changing a Git remote does not authorize retargeting an active environment operation. |
| Fork creation is pending or uncertain | Inspect the reported destination and retry the recorded approval. Do not delete the journal and blindly create again. |
| Fork setup reports an active/interrupted lock | Inspect the Git-local `radpack-fork.lock` and any running setup before removing it. Existing repositories and working files are preserved. |
| Latest mode reports conflicts | Follow `.radpack/ADAPT.md` and compare preserved upstream files with the reference assets. |
| App opening failed | Run `radpack open --directory <prepared-source>`. Do not repeat install into the existing directory. |
| No Radius handoff appears | Ensure the effective Radius extension supports the protocol, start a session, and use a linked worktree of the prepared clone. Do not install a duplicate over a development build. |
| Handoff reports a dirty worktree or wrong revision | Preserve user/setup changes and create a clean session worktree at the recorded source commit. |
| Handoff is claimed or partly applied | Inspect the original claiming worktree. Do not reset it or retry automatically. A new install directory creates a separate request. |
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
| `internal/radpack/authoring.go` | URL-first clone, Git-local authoring record, resume and progress |
| `internal/radpack/authoring_deployment.go` | Guided deployment choice within create and separate fork approval |
| `internal/radpack/fork.go` | Reviewed personal fork creation/reuse and recovery |
| `internal/radpack/repository_setup.go` | Reviewed independent snapshot publication, Actions guard, and recovery |
| `internal/radpack/deployment.go` | Separate local application/deployment identities |
| `internal/radpack/github_api.go` | Bounded GitHub API calls through the selected `gh` credential |
| `internal/radpack/build_sources.go` | Source-state checks and build warnings |
| `internal/radpack/integration_assets.go` | Explicit recipe roles and portability rules |
| `internal/radpack/readiness.go` | Build-source and recipe-reference assessment |
| `internal/radpack/report.go` | Machine-readable create and readiness reports |
| `internal/radpack/discovery.go` | Application, override, and catalog discovery |
| `internal/radpack/pack.go` | Pack validation and export |
| `internal/radpack/install.go` | Installation, receipts, verification, adaptation handoffs |
| `internal/radpack/handoff.go` | Private desktop staging, exact-worktree claims, consent-bound application |
| `internal/radpack/copilot_open.go` | Supported Copilot App launch command |
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

## What remains from the broader plan

| Capability | Current boundary |
| --- | --- |
| URL-first authoring and resume | Implemented: clone, pause, choose the prepared checkout, review, and save locally. Radification itself remains a user/agent task through Radius. |
| Agent progress | JSON reports expose waiting, selection, review, blocked, and saved states. They do not diagnose cloud credentials or run modeling/deployment jobs. |
| Save versus publish | Saving is implemented. A separate publish command, recipe publication, and catalog/upstream PR creation are not implemented. |
| Build upstream source plus a pack | Reports identify source-assembly gaps. The image-build path does not yet apply pack overlays automatically. |
| Latest-source adaptation | Reference assets and an explicit handoff are available. Autonomous repair and proof of compatibility are not implemented. |
| Copilot App integration | Install can open a session draft and the updated local Radius build can review/apply the pack. Automatic session start, plugin installation, and URL-create launch-to-authoring are not implemented. |
| Shared catalog and real examples | The bundled catalog is empty. A real community pack still needs a fresh consumer installation and deployment verification per provider. |
| Batch authoring across many repositories | Agents can call the commands individually. Scheduling, cost controls, resumable batch orchestration, and environment cleanup automation are not implemented. |
| Distribution trust | Hashes and policy checks exist. Pack signing, general binary payloads, and prebuilt CLI releases remain separate work. |

The [Radius-owned handoff protocol](docs/copilot-handoff.md) describes coordination
between this CLI and the companion Radius extension. End-to-end graph opening
requires that updated extension to be loaded; the Copilot App itself is unchanged.

## License

Radpack uses the [MIT license](LICENSE). Applications and recipes retain their
own licenses; pack authors must preserve required notices.
