# Contributing

Use Go 1.24 or newer and Git. Run `go fmt ./...`, `go vet ./...`, and
`go test ./...` before proposing CLI changes; run `go test -race ./...` for
concurrency checks. Keep the CLI free
of cloud side effects: create/install/verify must not provision environments,
register credentials, run payload scripts, or deploy.

## Contributing an application pack

Start with a manually prepared application. Do not submit a generated graph or
untested model as though it were a deployment-verified pack.

1. Choose the application repository consumers should clone.
2. Create the Radius model, reusable recipes, and any application adaptations.
   Keep the environment identifier and credentials as deployment inputs.
3. Verify the application in your author environment using the Radius tools.
4. Run `radpack create` in a terminal. Review the inferred published revision,
   select any required application adaptations, and confirm the export.
   For automation, use `--dry-run`, explicit selections, and `--approve`.
5. Install into a new directory and run `radpack verify` before building.
6. Use a separate consumer environment and a repository you control to test the
   installed application's graph, build, deployment, and primary user journey.
   Do not rely on the author's state, local files, private recipe access, or
   credentials.

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

Format v1 supports text payloads, inspected custom-type archives, and literal local Bicep dependencies. Do not
remove a required binary/custom-type extension or dependency to make a pack pass
validation. Report the unsupported requirement instead. Remote build sources
must contain the source adaptations the application needs.

No real application pack is included until this fresh-environment workflow has
been completed. Do not treat the synthetic test fixture as a runnable Radius
application or proof of cloud compatibility.

Create verifies source revisions in a scratch Git database, not the author's
checkout. Keep prompts terminal-only and confirmation default-no. Tests should
cover redirected streams, EOF/cancellation, local-only commits, fork ambiguity,
and file changes during review. Never assume that `main` is the default branch
or that all differences from upstream belong in a pack.

## Updating a pack to newer source

Use `radpack install owner/repo --latest --adapt` (with `--pack` for an unpublished
pack). Radpack produces a consumer checkout and an explicit Radius authoring
handoff. It does not update the published pack or invoke a model.

Resolve every reported conflict, validate through the Radius tools, inspect the
graph, and verify deployment independently per provider. Ensure remote image
builds consume the assembled source, including pack adaptations. Then export a
new pack from the reviewed checkout. A clean application of files must never be
presented as inherited deployment verification from the older reference revision.
