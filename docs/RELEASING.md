# Releasing

This document describes how to cut a v2 release and how to create release
branches for future maintenance.

## How releases work

Releases are driven entirely by git tags. Pushing a tag that matches `v*`
triggers the [Image workflow](../.github/workflows/image.yaml), which builds the
multi-arch `konnector` image and pushes it to the GitHub Container Registry:

```
ghcr.io/<owner>/konnector:<tag>
```

There is no `:latest` or `:<sha>` tag — the image registry is shared with the
v1/`main` images, so only the exact version tag is published.

Version tags follow [semantic versioning](https://semver.org):

- `v2.0.0-rc1`, `v2.0.0-rc2`, … — release candidates (pre-releases).
- `v2.0.0` — the final GA release.
- `v2.0.1`, `v2.1.0`, … — patch / minor releases.

## Cutting a release candidate

Release candidates are cut directly from `v2-next` (the v2 development branch)
until GA, then from the release branch (see below).

### The `release` helper

Rather than working out the next rc number by hand, use the
[`cmd/release`](../cmd/release) tool. It reads the tags already on the upstream
remote, finds the highest rc for the target version, and creates the next one:

```bash
# see what the next tag would be, without creating anything
go run ./cmd/release -dry-run

# create the next rc tag locally (e.g. v2.0.0-rc3)
go run ./cmd/release

# create AND push it (this triggers the Image workflow)
go run ./cmd/release -push
```

Flags: `-remote` (default `origin`), `-version` (default `v2.0.0`, the GA
version the candidates lead up to), `-push`, and `-dry-run`. The tag is created
on your current `HEAD`, so check out the commit you intend to release first.

### Cutting one by hand

1. Make sure your local checkout is up to date and on the branch you are
   releasing from:

   ```bash
   git checkout v2-next
   git pull --ff-only
   ```

2. Confirm CI is green for the commit you are about to tag.

3. Create an annotated tag and push it:

   ```bash
   git tag -a v2.0.0-rc1 -m "v2.0.0-rc1"
   git push origin v2.0.0-rc1
   ```

4. The Image workflow runs automatically. When it finishes, the image is
   available at `ghcr.io/<owner>/konnector:v2.0.0-rc1`.

5. (Optional) Create a GitHub Release from the tag and mark it as a
   pre-release:

   ```bash
   gh release create v2.0.0-rc1 --prerelease --generate-notes
   ```

Repeat with `-rc2`, `-rc3`, … as needed until the candidate is stable.

## Cutting the GA release

Once a release candidate is deemed stable, tag the same commit (or the tip of
the release branch) as the final version:

```bash
git tag -a v2.0.0 -m "v2.0.0"
git push origin v2.0.0
gh release create v2.0.0 --generate-notes
```

## Creating a release branch

Once `v2.0.0` ships, create a long-lived `release-2.0` branch so that
`v2-next`/`main` can move on to the next minor version while patch releases can
still be cut from the stable line.

1. Branch from the GA tag (or the commit you released):

   ```bash
   git checkout -b release-2.0 v2.0.0
   git push origin release-2.0
   ```

2. Wire the branch into CI so pushes and PRs against it run the test suite. In
   [.github/workflows/ci.yaml](../.github/workflows/ci.yaml), add the branch to
   both the `push` and `pull_request` branch lists:

   ```yaml
   on:
     push:
       branches:
         - main
         - v2-next
         - release-2.0
     pull_request:
       branches:
         - main
         - v2-next
         - release-2.0
   ```

   (The Image workflow triggers on `v*` tags regardless of branch, so it needs
   no change.)

### Patch releases from a release branch

Cherry-pick or merge fixes into `release-2.0`, then tag the patch version from
that branch:

```bash
git checkout release-2.0
git pull --ff-only
# ... land fixes here ...
git tag -a v2.0.1 -m "v2.0.1"
git push origin v2.0.1
gh release create v2.0.1 --generate-notes
```

## Naming conventions

| Kind             | Example        | Cut from        |
|------------------|----------------|-----------------|
| Release candidate| `v2.0.0-rc1`   | `v2-next`       |
| GA release       | `v2.0.0`       | `v2-next`       |
| Release branch   | `release-2.0`  | tag `v2.0.0`    |
| Patch release    | `v2.0.1`       | `release-2.0`   |
| Next minor branch| `release-2.1`  | tag `v2.1.0`    |
