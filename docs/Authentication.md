# Authentication

`ghr` asks GitHub for a short-lived *registration token* when a container starts and a *remove token* when it stops. It needs a credential that is allowed to do that. Pick one.

## Personal access token (simplest)

| Scope of the runners | Classic PAT | Fine-grained PAT |
|---|---|---|
| Repository | `repo` | Repository permission **Administration: read and write** |
| Organisation | `admin:org` | Organisation permission **Self-hosted runners: read and write** |
| Enterprise | `manage_runners:enterprise` | not supported |

Use a dedicated machine account, not a personal one.

## GitHub App (recommended for organisations)

Tokens are minted on demand and expire after an hour, nothing long-lived sits in your `.env` except the app's private key.

1. Create an app with **Self-hosted runners: read and write** (organisation permission) for org runners, or **Administration: read and write** (repository permission) for repo runners.
2. Install it on the organisation or repository.
3. Generate a private key and set:

```env
APP_ID=123456
APP_PRIVATE_KEY_FILE=/run/secrets/app_key   # or APP_PRIVATE_KEY with literal \n
```

`APP_ID` may also be the app's Client ID (`Iv1...`). Enterprise runners cannot use a GitHub App; use a PAT.

## Keeping secrets away from workflows

`ghr` removes `ACCESS_TOKEN`, `RUNNER_TOKEN`, `APP_ID`, `APP_LOGIN` and `APP_PRIVATE_KEY` (and their `_FILE` forms) from the environment of the runner process. Workflow steps inherit that environment, so without this a job could simply print the token. (The upstream image keeps them unless you opt in to `UNSET_CONFIG_VARS`.)

Files mounted for `*_FILE` secrets are still readable by the `runner` user inside the container; see [Security](Security).
