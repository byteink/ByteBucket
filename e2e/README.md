# Browser E2E (admin panel)

Playwright tests that drive the real React admin UI in Chromium against a
running ByteBucket container. Kept in its own package so the production image
build (`web/`) never pulls Playwright or its browser binaries.

## Run

```bash
make e2e-web        # from repo root: builds a fresh container, runs the suite, tears it down
```

Or against an already-running instance:

```bash
cd e2e
npm ci
npx playwright install chromium
BB_ADMIN_URL=http://localhost:9001 npx playwright test
```

## Configuration

Environment overrides (all optional; defaults target the local-compose superuser):

- `BB_ADMIN_URL` — admin UI origin (default `http://localhost:9001`)
- `BB_ADMIN_AK` / `BB_ADMIN_SK` — admin credentials

## Notes

- The suite runs serially (one worker): every test shares one backend with one
  user store, so parallelism would cross-contaminate state.
- Specs that mutate persisted settings (fsync, retention) toggle relative to the
  current value and restore it; `make e2e-web` also starts each run from a clean
  volume.
- Coverage: login/auth guard/logout, dashboard cards + request chart navigation,
  per-bucket table, user create/delete, durability and retention settings.
