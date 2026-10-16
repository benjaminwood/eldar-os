# Staging and publishing

Two repos with the same history:

| Repo | Visibility | Role |
|---|---|---|
| `eldar-os-staging` | private | Source of truth. All work lands here: Ben's, Eldar's PRs, experiments. The machine applies from here. |
| `eldar-os` | public | A published copy of staging `main`, up to whatever point was last published. Never committed to directly. |

Locally, `origin` is staging and `public` is the public repo:

```bash
git clone git@github.com:benjaminwood/eldar-os-staging.git eldar-os
git -C eldar-os remote add public git@github.com:benjaminwood/eldar-os.git
```

## Publishing

```bash
scripts/publish             # everything on staging main
scripts/publish <commit>    # up to a specific commit
```

It only ever fast-forwards the public `main` with commits that are already on
staging `main`, and scans every unpublished commit with gitleaks first.

Unpublished history can still be rewritten, and that's the point of staging:
if something that shouldn't be public lands in a commit, remove it from history
(rebase, then force-push staging) before publishing. A follow-up commit that
deletes it is not enough; the original commit would still be published.

## What stays out of both

Staging is published eventually, in full. So anything that must never be
public (secrets, personal data, Eldar's memory and persona) doesn't go in
staging either. It lives in a separate private repo that this flake takes as
an input (not created yet), or outside git entirely.

## CI

The same workflow runs in both. `build` runs on every push in both. `e2e`
runs on every push and nightly in the public repo (public repos get free,
larger runners); in staging the Actions variable `E2E=manual` makes it
on-demand (`gh workflow run ci -R benjaminwood/eldar-os-staging`).
