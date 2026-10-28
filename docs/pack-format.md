# Pack manifests and installation records

## Portable pack format v1

A pack is a directory containing exactly `radpack.json` and the declared payload
files beneath `overlay/`. No archive extraction or install scripts are involved.
Unknown manifest fields, undeclared files/directories, symlinks, special files,
unsafe paths, and case-colliding paths are rejected.

```text
my-app/
  radpack.json
  overlay/
    .radius/
      app.bicep
      bicepconfig.json
    src/
      frontend/
        nginx.conf
```

The manifest has this shape (hash placeholders below are illustrative):

```json
{
  "formatVersion": 1,
  "name": "my-app",
  "upstream": {
    "repository": "https://github.com/example/my-app.git",
    "commit": "<40 lowercase hexadecimal characters>"
  },
  "application": ".radius/app.bicep",
  "radiusVersion": "0.60.0",
  "files": [
    {
      "path": ".radius/app.bicep",
      "operation": "add",
      "before": null,
      "after": {
        "sha256": "<64 lowercase hexadecimal characters>",
        "mode": "100644"
      }
    }
  ]
}
```

`radiusVersion` is an optional author-provided release annotation, represented as
`null` when unspecified. It is not an automatically verified compatibility claim.
The application path must be `.radius/app.bicep` or `app.bicep`.

Every selected file has a `before` fingerprint from the upstream Git blob and an
`after` fingerprint from the prepared working tree. A fingerprint contains a
SHA-256 of the exact bytes and Git mode `100644` or `100755`.

| Operation | Before | After | Overlay payload |
| --- | --- | --- | --- |
| `add` | `null` | Fingerprint | Required |
| `replace` | Fingerprint | Different fingerprint | Required |
| `delete` | Fingerprint | `null` | Absent |
| `retain` | Fingerprint | Identical fingerprint | Absent |

Unchanged selected files are recorded as `retain`. This lets the same format
support assets that already live in the upstream application repository, and
binds local dependencies to the pinned revision without copying them.

File operations are sorted by their case-sensitive paths on export. Paths use
forward slashes, contain printable ASCII only, and are relative to the application checkout. Parent traversal,
absolute paths, `.git` segments, Windows device names, alternative data streams,
Git short-name aliases, and overlapping file/parent paths are prohibited.

## Review and integrity

The preview's approval SHA covers the manifest, excluded-file inventory,
unselected-file inventory, and policy blockers. Publication rechecks that SHA and
the selected payload fingerprints. A fresh create invocation recomputes the review
from the current working tree; a prior approval cannot silently approve new bytes.

Payloads are full resulting files, not patches. Original content is represented
only by its fingerprint, so deleting a credential from a source file does not
copy that credential into a diff's removed lines.

Installation checks the entire pack before creating a destination, then verifies
every upstream precondition before applying any changes. All retained files are
also scanned against the portable-content policy after cloning.

## In-repository asset manifest

Applications can keep the integration upstream without duplicating `.radius/`
files in an overlay. Place this descriptor at `.radpack/radpack.json`, or in a
directory explicitly selected with `--pack-path`:

```json
{
  "formatVersion": 1,
  "kind": "in-repository",
  "name": "my-app",
  "application": ".radius/app.bicep",
  "radiusVersion": "0.60.0",
  "assets": [
    ".radius/app.bicep",
    ".radius/bicepconfig.json",
    ".radius/custom-recipe-pack.bicep"
  ]
}
```

List only assets that actually exist. Paths are relative to the **application
repository root**, not the descriptor directory. The explicit `application`
must appear in `assets` and match the model discovered in the checkout.
Standard companion assets and literal local dependencies are collected using
the same portability policy as `create`. Arbitrary lifecycle hooks and unknown
descriptor fields are not supported.

The descriptor intentionally has no commit field: a file cannot name the commit
that contains its own final bytes. Discovery resolves the default branch once;
that exact commit is both the reference application revision and the pack source.
Radpack materializes a portable manifest using `retain` entries for the existing
files and records the descriptor's original hash and location in the receipt.
The portable materialization is stored locally for inspection and verification;
there is no duplicated overlay committed upstream.

An external portable pack still pins its own upstream reference commit. These
two forms are distinguished by the descriptor's `kind: "in-repository"` field.
`--pack` selects a portable pack directory; `--pack-path` selects an in-repository
descriptor directory. A GitHub tree URL selects a portable external pack, not a
descriptor for a different application.

## Bundled catalog

`catalog.json` maps lowercase application identities to immutable external pack
locations. It ships with the CLI; it is not downloaded from a mutable catalog at
installation time.

```json
{
  "formatVersion": 1,
  "applications": {
    "example/my-app": "https://github.com/example/packs/tree/<40-character-commit>/contrib/my-app"
  }
}
```

The initial catalog is empty. Add entries only for real packs. An explicit
override takes precedence, then an in-repository descriptor, then the catalog.
Malformed or unsafe sources do not trigger fallback. Source repository identity
must match the application's identity (case-insensitive).

## Installation receipt v2

New installations write `.radpack/install.json` with:

```json
{
  "formatVersion": 2,
  "packManifestSha256": "<hash of the exact radpack.json bytes>",
  "manifest": "<the parsed portable manifest object>",
  "applicationCommit": "<exact installed application commit>",
  "packSource": {
    "kind": "github",
    "repository": "https://github.com/example/packs.git",
    "commit": "<exact pack repository commit>",
    "path": "contrib/my-app",
    "manifestSha256": "<hash of the source manifest bytes>"
  },
  "status": "installed",
  "files": [
    {
      "path": ".radius/app.bicep",
      "after": { "sha256": "<exact installed bytes>", "mode": "100644" }
    }
  ],
  "metadata": [
    {
      "path": ".radpack/pack/radpack.json",
      "sha256": "<exact saved bytes>"
    }
  ]
}
```

`packSource.kind` is `local`, `github`, or `in-repository`. Local sources record
the manifest hash without copying a machine-specific absolute path. GitHub and
in-repository sources record repository, commit, path, and manifest hash. For an
in-repository source, that hash identifies the descriptor, not the materialized
portable manifest (whose hash is in `packManifestSha256`).

`files` records the installed snapshot; `after: null` means the file is absent.
`metadata` hashes all generated receipt-adjacent files, including the saved pack.
It does not hash the receipt itself. This is corruption detection, not an
authenticated journal; a party able to rewrite all hashes can forge the record.
Legacy v1 receipts remain verifiable when their original pack directory is supplied.

Matching installations have status `installed` and use the pack reference commit.
Latest installations always have status `adaptation-required`, even if no conflicts
were found or latest equals the reference. Their source commit may differ from
the reference. They preserve changed/deleted upstream files rather than applying
stale overlays to them. Original portable assets, changed-path inventory, and
explicit human/Radius instructions are saved alongside the receipt.

No automatic model validator or deployment verifier is invoked, and there is no
status-promotion command. The only statuses emitted by this CLI are `installed`
and `adaptation-required`; neither is a claim of model or deployment validity.

This receipt is local provenance, not credential storage or cloud deployment state.
Checksums are not signatures. Changing both a payload and its manifest can create
a different valid pack; distribution still requires publisher trust.

## Portability contract

The pack describes an application, not an environment instance. Consumers provide
their environment identifier, cloud identity, infrastructure selections, registry
credentials, and application secrets through Radius.

Reusable recipe definitions and required inputs may be included. Concrete
environment resources, generated provider-pack copies, credentials, state, and
deployment-specific parameter values must not be included. The parser and policy
in `internal/radpack/pack.go`, `internal/radpack/types.go`, and
`internal/radpack/policy.go` are authoritative for format v1. The native Go
implementation reads the same pack and receipt formats as the original JavaScript
CLI. JSON parsers reject duplicate keys, unknown fields, missing required fields,
and invalid field types; raw JSON values are not echoed in errors.

Review approval hashes are temporary approvals for the current CLI's preview,
not part of the portable manifest contract. Re-run the preview after upgrading
implementations before approving an export.

Custom-type `.tgz` payloads are carried intact but inspected without extraction
before export/import. Members must be safe, non-sensitive UTF-8 regular files or
directories, within the documented size/count limits. General binary files are
not supported.

This version deliberately has no lifecycle hooks, remote mutable catalog, automatic
semantic merging/agent invocation, or provider certification field. Future format
changes must preserve the environment boundary and fail closed on unsupported
versions rather than guessing their meaning.
