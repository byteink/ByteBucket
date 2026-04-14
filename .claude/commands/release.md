---
name: release
description: Cut a ByteBucket release — preflight, tag vX.Y.Z, and push so the release workflow builds and publishes the Docker image.
argument-hint: "[patch|minor|major] or [explicit version e.g. 1.2.0]"
---

Cut a new release for ByteBucket. `$ARGUMENTS` is either a semver bump type (`patch`, `minor`, `major`) or an explicit version (e.g. `1.2.0`). If empty, default to `patch`.

Version lives in git tags, not in a file. The GitHub Actions workflow at `.github/workflows/release.yml` triggers on tags matching `v*`, builds a multi-arch Docker image, pushes it to `ghcr.io/byteink/bytebucket`, and creates a GitHub Release with auto-generated notes.

## Steps

### 1. Determine the next version

- Run `git describe --tags --abbrev=0 2>/dev/null` to get the latest tag.
- If no tag exists yet, treat current as `v0.0.0`.
- Compute next version from `$ARGUMENTS`:
  - `patch` (default): `v0.1.0` → `v0.1.1`
  - `minor`: `v0.1.0` → `v0.2.0`
  - `major`: `v0.1.0` → `v1.0.0`
  - Explicit (e.g. `1.2.0` or `v1.2.0`): normalise to `v1.2.0` after validating semver.
- Show the user:
  - Current latest tag
  - Proposed new tag
  - The commit list that will be shipped (`git log <prev-tag>..HEAD --oneline --no-merges`)
- Ask for confirmation before proceeding. Do not push without it.

### 2. Preflight checks

Stop and report on the first failure:

- Current branch must be `main`: `git rev-parse --abbrev-ref HEAD`.
- Working tree must be clean: `git status --porcelain` is empty.
- Local main must be up to date with remote:
  ```
  git fetch origin main
  git diff HEAD origin/main --quiet
  ```
- Tag must not already exist locally or remotely:
  ```
  git rev-parse -q --verify "refs/tags/vX.Y.Z" && echo "tag exists locally"
  git ls-remote --tags origin "vX.Y.Z" | grep -q . && echo "tag exists on remote"
  ```
- Tests must pass: `go test -count=1 ./...` (runs the full suite including the testcontainers E2E — it takes ~30-60s on a warm cache).
- `go vet ./...` must be clean.

If anything fails, surface it clearly and stop. Do not try to "fix" a dirty tree or a stale branch by stashing / rebasing silently.

### 3. Tag and push

Only after confirmation and passing preflight:

```
git tag -a "vX.Y.Z" -m "release: vX.Y.Z"
git push origin "vX.Y.Z"
```

The annotated tag (`-a`) is intentional — it carries tagger info and a message, which GitHub displays on the release page. Do not use lightweight tags.

### 4. Watch the workflow

After pushing, report the GitHub Actions URL so the user can follow along:

```
gh run watch --exit-status || true
```

Or just print the URL to the run:

```
gh run list --workflow=release.yml --limit=1 --json databaseId,url --jq '.[0].url'
```

If `gh run watch` is available, prefer that — it streams the run and returns non-zero on failure so the user knows if the build failed.

### 5. Summarise

On success, report:
- Tag name
- Image reference that will be pulled by consumers (e.g. `ghcr.io/byteink/bytebucket:vX.Y.Z` and `:latest`)
- Link to the created GitHub Release (the workflow creates it; after the run completes: `gh release view vX.Y.Z --json url --jq .url`)

On failure during preflight, report which check failed and do NOT tag. The user fixes the cause and re-runs `/release`.

## Hard rules

- Never bypass the preflight, even if the user pushes back. If tests are red, the release is broken — surface it, do not tag.
- Never force-push a tag.
- Never delete an existing remote tag to "fix" a version number. Bump to the next version instead.
- Never create a release from a branch other than `main`.
- Never run this against an uncommitted working tree.
- Do not edit `.github/workflows/release.yml` as part of this command. Workflow changes go through a normal commit/PR flow.

## Pre-release versions

If the user asks for a pre-release (e.g. `v1.0.0-rc.1`), accept it. The workflow recognises the hyphen, excludes it from the `latest` / major / minor rotation, and marks the GitHub Release as a pre-release. You do not need to do anything special here beyond computing / validating the version string.
