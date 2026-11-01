# Radius-owned desktop handoff

Radpack and the Radius extension coordinate installation without changing the
GitHub Copilot App. The app's supported `github open --new <directory>` command
opens a session draft. The user starts a session through the app's normal flow;
the Radius extension then offers to apply the pack in that session's worktree.

The companion Radius extension must implement the protocol below. An older
extension can open graphs normally but will not recognize pending handoffs.
Radpack does not replace or upgrade local development builds.

The local CLI accepts portable pack formats v1 and v2. Recipe-role metadata in
v2 remains in the saved manifest and installation receipt; it does not alter this
handoff protocol or authorize environment registration. The review's file
operations still describe the actual source-to-installed delta.

## User flow

```bash
cd ~/Source
radpack install owner/application
```

Radpack resolves the application and pack revisions, prepares and verifies the
assembled application in private local staging, and clones the selected source
into a new directory under the current directory. It leaves this source clone
clean and opens it in the Copilot App.

The user must:

1. Complete app sign-in or repository trust prompts when required.
2. Start an app session in a linked worktree at the recorded source revision.
3. Review the Radius handoff and approve applying the pack.

The app may use its default branch when creating the worktree. If that differs
from the pack's recorded revision, the handoff refuses to apply; choose the
recorded source revision rather than silently updating the pack.

Radius checks for a locally staged request associated with the clone. It never
treats a file in downloaded source as authorization. After explicit approval,
Radpack applies the staged result to the worktree, verifies it, and returns the
exact model path and fingerprint. Radius can then render the existing model.
This does not establish deployment readiness.

The app session exists before the extension runs. Repository setup may therefore
have already run. Dirty worktrees, including ignored build artifacts, block
application instead of being overwritten. Instructions or scripts added by a
pack remain subject to the app's normal trust and reload rules.

## Plugin setup

The handoff is handled by the effective Radius extension in the session, whether
it comes from awesome-copilot or a local development build. A missing, disabled,
or policy-blocked plugin is not permission to install a duplicate.

If Radius is absent, the user installs it through the app's Customize page and
its normal approval flow. If a local development build is selected, update that
build deliberately rather than replacing it. Radpack does not infer the app's
effective plugins from the standalone Copilot CLI's plugin list.

This implementation does not automatically install software, start a session,
accept trust prompts, send an agent prompt, or create an environment.

## Terminal-only installation and resume

```bash
radpack install owner/application --no-open
```

`--no-open` uses the direct installer: the new checkout contains the overlay and
receipt immediately. It does not create handoff state, inspect plugins, or launch
the app.

If opening the app fails after desktop preparation:

```bash
radpack open --directory /absolute/path/to/prepared-source
```

This retries the app-opening step without cloning or applying files again.
`RADPACK_GITHUB_COMMAND` may identify an absolute, operator-selected Copilot App
executable. Otherwise Radpack uses `github` from PATH, or the standard macOS
application executable when present. A binary inside downloaded source is
refused. A successful open command confirms only that the app accepted the
directory, not that a session or graph is ready.

## Local state

State lives under:

```text
~/.radpack/handoffs/<sha256-of-canonical-git-common-directory>/
  request.json
  candidate/       verified assembled application and saved portable pack
  claim.json       created exclusively when a worktree claims the handoff
```

The parent and request directories are created privately. Unix state directories
must not be writable by other users, and symlinked state paths are refused.
Windows relies on the user's profile access controls; Go permission bits are
not treated as Windows ACL verification.

The request records an opaque random ID, the source clone's canonical directory,
Git common directory, selected commit, pack hash, installation status, creation
time, and expiration time. A matching ID in the clone's local Git metadata binds
the request to that clone rather than merely to a path that could be reused.
No account credentials, cloud environment values, or deployment state are stored.
The staged application is subject to the same portability checks as direct
installation, not a claim that arbitrary upstream source is secret-free.

Pending requests expire after seven days. Successful application can still be
recognized after expiry. State is retained for inspection and recovery; this
version does not automatically garbage-collect it.

## CLI protocol v1

The extension resolves an operator-trusted Radpack executable. It must not
execute a path or command from application source or the handoff payload.

### Inspect without mutation

```text
radpack handoff status --directory <absolute-session-worktree> --json
```

The command returns one JSON object. A checkout with no local request returns:

```json
{
  "protocolVersion": 1,
  "status": "none",
  "warnings": []
}
```

Pending and applied responses include:

| Field | Meaning |
| --- | --- |
| `protocolVersion` | `1` |
| `status` | `pending`, `applied`, or `blocked` |
| `requestId` | Opaque request identifier |
| `reviewSha256` | Approval fingerprint for this exact worktree; preserved in successful apply responses and applied status |
| `repository` | Canonical source repository URL |
| `applicationCommit` | Exact required source commit |
| `packManifestSha256` | Hash of the portable pack manifest |
| `modelPath` | `.radius/app.bicep` or `app.bicep` |
| `modelSha256` | Hash of the staged assembled model |
| `installationStatus` | `installed` or `adaptation-required` |
| `sourceDirectory` | Canonical clean source clone path |
| `worktreeDirectory` | Canonical session checkout path |
| `files` | Actual selected-source-to-staged-result operations: `add`, `replace`, `delete`, `retain`, with original/result fingerprints |
| `warnings` | Review warnings or blocking explanations |

Blocked responses can omit a model hash when the selected latest source has
deleted the model. A missing hash must never be interpreted as graph-ready.
All hashes are lowercase hexadecimal SHA-256 strings, without a `sha256:` prefix.
Before application, the review fingerprint binds the returned context to the
worktree, request, selected revision, model, operations, and warnings.
For latest-source conflicts, the review describes the preserved upstream file as
`retain`, rather than suggesting the older pack's replacement will be applied.

The original source clone can be inspected for launch/resume guidance, but cannot
receive the overlay through this protocol. Application requires a linked worktree
sharing the exact Git common directory.

### Apply after consent

```text
radpack handoff apply --directory <worktree> --request-id <id> --approve <review-sha> --json
```

The extension must show the review and receive explicit user approval before
calling this operation. A request ID is not authorization by itself.

Radpack checks the clone identity, HEAD, origin, clean worktree, staged content,
and review fingerprint. It creates an exclusive persisted claim before writing.
Only one worktree may claim a handoff. It rereads target files before applying
changes and uses the verified candidate assembled by the existing installer.

Successful application returns `applied` with the approved `reviewSha256` and
leaves a normal Radpack receipt in
the worktree. Repeating application in the same successfully verified worktree
is idempotent. Another worktree cannot consume the same request.

A failed or interrupted partial application remains claimed and blocked. It is
not automatically retried or rolled back over possible user edits. The extension
must explain the failure and leave the worktree for inspection. It must never
delete an app-managed worktree or run a broad clean/reset operation.

CLI failures return nonzero with diagnostics on stderr. Consumers must not treat
missing/malformed JSON, unknown protocol versions, or process success without
`applied` as successful installation.

## Graph rendering

The companion Radius extension uses an opt-in existing-model-only graph path:
verify the returned model hash, render the current worktree model, and do not
queue model generation, repair, deployment, or public exposure automatically.
Missing, changed, stale, or invalid models require a visible diagnostic.

`adaptation-required` remains visible for latest-source installs. A rendered
reference graph does not establish that the integration matches the latest
application. The extension must not change the receipt status or fabricate
freshness evidence.

## Validation boundary

Go regressions cover clean source preparation, exact-worktree consent, claims,
tampering, expiration, dirty worktrees, resume, direct `--no-open` installs, and
machine-readable protocol responses. App launching is exercised through test
executables; no test should open a user's app or install a plugin.

Companion Radius tests must cover its startup probe, explicit review, cancellation,
protocol validation, duplicate suppression, and non-authoring graph behavior.
An actual app-session round trip requires the updated Radius extension to be
loaded. Neither a Go unit test nor `github open` acknowledgment proves that the
desktop session displayed the graph.
