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
scripts/publish --review             # unpublished commits → a file to review
scripts/publish --reviewed <commit>  # publish up to the reviewed commit
```

Three checks before anything is public:

1. **A review** of the unpublished commits (messages and diffs) for what must
   not be public that no scanner recognizes: personal data, private
   infrastructure (tailnet IPs, ACL rules, account ids, 1Password profile
   names), an employer's internals, anything copied from the private repos,
   unfixed weaknesses. `--review` writes the commits to a file and lists what
   to look for; a reviewer with fresh context (a person or an agent told only
   what this is) reads it. Publishing takes the reviewed commit, so anything
   that lands after the review needs a new one.
2. **A deny list** of strings that must never be public (the tailnet's name,
   device names): `publish-denylist` in the secrets repo, private because it
   names what it protects. Any hit in a commit message, diff or published file
   stops the publish (and `--review`), reported by commit and file only. A
   missing list stops it too.
3. **gitleaks** scans every unpublished commit for secrets, at publish.

It only ever fast-forwards the public `main` with commits that are already on
staging `main`. Unpublished history can still be rewritten, and that's the
point of staging: if something that shouldn't be public lands in a commit,
remove it from history (rebase, then force-push staging) before publishing. A
follow-up commit that deletes it is not enough; the original commit would
still be published.

## What stays out of both

Staging is published eventually, in full. So anything that must never be
public doesn't go in staging either. It goes in the private
`eldar-os-secrets` repo, which the machine fetches alongside staging: sops
secrets, the supervisor's policy, remote hosts (`supervisor/hosts.json`), AWS
profiles, and private notes (`notes/`). Eldar's persona and memory live in its
workspace, outside both.

## CI

The same workflow runs in both. `build` runs on every push to `main` in both (not on PRs: the machine builds before applying). `e2e`
runs on every push and nightly in the public repo (public repos get free,
larger runners); in staging the Actions variable `E2E=manual` makes it
on-demand (`gh workflow run ci -R benjaminwood/eldar-os-staging`).
