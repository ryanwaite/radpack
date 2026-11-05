# Contributing

Use Go 1.24 or newer and Git. Run `go fmt ./...`, `go vet ./...`, and
`go test ./...` before proposing CLI changes; run `go test -race ./...` for
concurrency checks. Keep the CLI free
of cloud side effects: create/install/verify must not provision environments,
register credentials, run payload scripts, or deploy.

Personal fork setup is an explicitly approved GitHub operation, separate from
create/save and publish. Develop it against mocked GitHub responses and temporary
Git repositories. Never create a contributor's real fork, enable Actions, push
prepared source, or run cloud readiness probes as part of tests. Preserve the
original source identity in packs and the deployment repository only in local
Git metadata. Test account drift, repository replacement, remote conflicts,
partial failures, and retry without duplicate creation.

## Contributing an application pack

Start with a manually prepared application. Do not submit a generated graph or
untested model as though it were a deployment-verified pack.

1. Choose the application repository consumers should clone.
   `radpack create owner/repo` can clone it and record the baseline before
   preparation; use `--resume <checkout>` to return later. Existing prepared
   checkouts can still use plain `radpack create`.
2. Create the Radius model, reusable recipes, and any application adaptations.
   Keep the environment identifier and credentials as deployment inputs.
3. Verify the application in your author environment using the Radius tools.
4. Run `radpack create` in a terminal. Review the inferred published revision,
   select any required application adaptations, and confirm the export.
   For automation, use `--dry-run`, explicit selections, and `--approve`.
5. Install into a new directory with `--no-open` and run `radpack verify` before
   building, or approve the desktop handoff and verify the resulting app worktree.
6. Use a separate consumer environment and a repository you control to test the
   installed application's graph, build, deployment, and primary user journey.
   Do not rely on the author's state, local files, private recipe access, or
   credentials.

Use `--recipe <file>` and `--recipe-pack <file>` to declare reusable integration
entry points. These roles produce format v2 and remain available in the installed
manifest. Include all required Terraform module files and scripts explicitly;
local dependency discovery is Bicep-specific. Parameterized recipe-pack
definitions may be shared, but concrete author environments and secret values
must remain outside the pack.

Use `radpack create --json` for agent-readable review and
`radpack report <pack-directory>` for build-source and recipe findings.
`radpack report --directory <installed-checkout>` can inspect retained source
assets without guessing their contents. Treat `reference-checks-passed` as a
limited static result, never as compilation or deployment evidence.

Pack creation saves locally. Recipe publication, application-source publication,
and a catalog PR are separate actions requiring the user's chosen destination
and approval. Do not add a new pack archive format for these workflows.

Place the resulting pack under `contrib/<application>/`. The directory must
contain only the format-defined manifest and payloads; keep review commentary in
the pull request, not as undeclared files in the pack.

For catalog discovery, add a lowercase `owner/repo` entry in `catalog.json` pointing
to the pack directory at an exact commit in its publishing repository. Do not
add a placeholder catalog entry for an app that has no usable pack yet.

If the application adopts Radius upstream, prefer an in-repository manifest at
`.radpack/radpack.json` pointing to its existing assets. See the format reference.
This descriptor is source-controlled; installation receipts and adaptation context
under the same directory are local artifacts, not pack inputs.

In the pull request, record the pack manifest SHA from `radpack inspect`, Radius
release, provider tested, test date, and what user journey passed. Mark untested
providers explicitly. Do not include subscription/account identifiers,
credentials, state packages, kubeconfigs, or raw deployment logs. A successful
installation is not evidence of a successful deployment.

An independent reviewer should reproduce the install and inspect the selected
content. Best-effort secret checks are defense in depth, not a replacement for
review or your organization's secret scanner.

## Current acceptance boundary

Formats v1/v2 support text payloads, inspected custom-type archives, and literal
local Bicep dependencies; v2 additionally records recipe roles. Do not
remove a required binary/custom-type extension or dependency to make a pack pass
validation. Report the unsupported requirement instead. Remote build sources
must contain the source adaptations the application needs.

No real application pack is included until this fresh-environment workflow has
been completed. Do not treat the synthetic test fixture as a runnable Radius
application or proof of cloud compatibility.

For desktop handoff changes, test with isolated local-user state and simulated
app executables. Never open a contributor's live app or install plugins from a
unit test. State belongs outside the repository. A handoff must match the clone's
Git identity, selected commit, and exact worktree review before applying files.
Partial application stays claimed and visible; do not remove an app-managed
worktree to recover. Coordinate protocol changes with the Radius extension.

Create verifies source revisions in a scratch Git database, not the author's
checkout. Keep prompts terminal-only and confirmation default-no. Tests should
cover redirected streams, EOF/cancellation, local-only commits, fork ambiguity,
and file changes during review. Never assume that `main` is the default branch
or that all differences from upstream belong in a pack.

URL-first authoring keeps its record in Git-local metadata. Exercise restart
from another current directory, EOF/cancellation, saved-output verification,
baseline preservation, and worktree selection. Never infer that a graph file
means deployment verification, and never publish as a side effect of save.
Use mock GitHub APIs for the default deployment stage: prove fork-first creation
does not query upstream administration, while `--use-source` requires it and
never falls back to a fork. Cover retained legacy choices, unavailable/SAML
access, separate fork approval, named/reused forks, remembered local-only choice,
and pending-operation retries. `--status` must remain
read-only, and neither `--fork` nor pack-save approval may create a fork without
fork consent. Local-only authoring fixtures should explicitly use `--no-fork`
so they cannot depend on a contributor's live GitHub account.

## Updating a pack to newer source

Use `radpack install owner/repo --latest --adapt` (with `--pack` for an unpublished
pack). Radpack produces a consumer checkout and an explicit Radius authoring
handoff. It does not update the published pack or invoke a model.

Resolve every reported conflict, validate through the Radius tools, inspect the
graph, and verify deployment independently per provider. Ensure remote image
builds consume the assembled source, including pack adaptations. Then export a
new pack from the reviewed checkout. A clean application of files must never be
presented as inherited deployment verification from the older reference revision.
