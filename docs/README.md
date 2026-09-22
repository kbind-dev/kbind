# Documentation and publishing

The v2 site keeps the original MkDocs Material theme, **mike** versioning,
and directory-based navigation using the **awesome-pages** plugin.
Content lives under `docs/content`, each section's `.pages` file controls
its navigation. Only this directory is published, design proposals and
release-maintenance notes remain repository documents.

## Build and preview

Use Python with `venv` and the Go version from `go.mod`. CI uses Python
3.12. From the repository root:

```bash
make docs-venv
make build-docs
make serve-docs
```

The first command installs dependencies into the ignored `docs/.venv`.
`build-docs` generates references and runs a strict MkDocs build.
`serve-docs` serves a local preview at `http://127.0.0.1:8000`, it does not
modify any published version or redirect.

The API generator scans this checkout's `sdk/apis` using a pinned
`crd-ref-docs` version. The CLI generator uses this checkout's Cobra
command tree. Generated references and the built site are ignored by git.
After changing API types or CLI definitions, restart the preview or run
`make generate-docs` again.

## Non-default v2 publication

The preview is always published as **`v2`**, displayed in the
version selector as **v2 (preview)**:

<https://docs.kbind.dev/v2/>

Publication deliberately does **not** derive the version from the newest
tag, assign the `latest` alias, call `mike set-default`, or replace the
root redirect. The existing 0.x versions, `latest`, and custom domain
stay in the `gh-pages` branch.

The **Docs** workflow (`.github/workflows/docs-gen-and-push.yaml`)
builds relevant pull requests without deployment. Pushes to `v2-next`
or the temporary `docs-v2` preparation branch, and manual dispatches on
those branches, can publish only in `kbind-dev/kbind` once the workflow
is committed there. Forks build but
cannot publish to the canonical site. Publication shares the `Docs`
concurrency group with the stable docs workflow and never force-pushes.

## Publish a checkout manually

Use a clean checkout containing the intended documentation, review its
build, and confirm the selected remote is the canonical repository. For
example, if it is named `upstream`:

```bash
git remote get-url upstream
make docs-venv
REMOTE=upstream make deploy-docs
git diff --stat upstream/gh-pages..gh-pages
git diff upstream/gh-pages..gh-pages -- versions.json index.html CNAME latest
git push upstream gh-pages:gh-pages
```

`deploy-docs` fetches the existing publishing branch and makes a local mike
commit. **It does not push by default.** Review that only `v2/` and the
new `versions.json` entry changed before pushing. If someone publishes in
the meantime, fetch and rebuild on the updated branch rather than
force-pushing.

CI passes `PUSH=1` to publish immediately after the strict build.
`REMOTE` defaults to `origin` and `BRANCH` to `gh-pages`, override them
only deliberately. There is intentionally no `VERSION` or default-version
switch in the preview publisher. Making v2 the stable default is a
separate release decision.
