# Publishing a release

v2 releases are driven by version tags. The Image workflow publishes versioned `konnector` and `backend` images plus the `konnector-v2` and `backend-v2` OCI charts. Final tags also publish CLI archives, prerelease tags do not update krew.

## Release candidates

From the intended checkout, inspect the next release-candidate tag:

```bash
go run ./cmd/release -remote upstream -dry-run
```

Only when ready to create and publish it, use the helper's `-push` option. The remote must point to the intended release repository.

See the maintained [release procedure](https://github.com/kbind-dev/kbind/blob/v2-next/docs/RELEASING.md) for manual tags, final releases, and maintenance branches.

## Documentation

The v2 docs publisher uses `v2` without changing the site's root redirect or `latest` alias. Tags do not automatically promote v2 documentation to the default. Local build and publishing instructions live in `docs/README.md`.
