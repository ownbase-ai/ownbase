# ownbase.ai

Static marketing site for OwnBase. Astro + Tailwind 3, design tokens shared
with the desktop app via [`shared/theme/preset.mjs`](../shared/theme/preset.mjs).

The site is a document, not an app: it never calls a backend, never opens
a websocket, and ships essentially zero client JS (one clipboard handler
for the three "Copy URL" buttons).

## Develop

```bash
cd site
npm install
npm run dev        # http://localhost:4321
```

```bash
npm run build      # astro check + static build → dist/
npm run preview    # serve dist/
```

From the repo root:

```bash
make site          # dev server
make site-build    # production build
make site-check    # typecheck + build (what CI runs)
make site-publish  # build + upload to ownbase.ai
```

`site-publish` defaults to `SITE_BUCKET=ownbase-ai` and `AWS_PROFILE=personal`.
Override either as an env var.

## Layout

| Path | What |
|---|---|
| `src/pages/index.astro` | The landing page — all nine sections |
| `src/layouts/Base.astro` | `<head>`, OG/Twitter, JSON-LD |
| `src/components/` | Header, Footer, CopyUrl, Icon, AppScreenshot, WorksWith |
| `src/data/comparison.ts` | SaaS cards + Base checklist (one source of truth) |
| `src/styles/global.css` | Site base styles (no app chrome) |
| `public/llms.txt` | Agent install runbook — primary CTA |
| `public/og.png` | 1200×630 social card |

## Sharing with the desktop app

Colors, mono stack, and the fade-in animation live in
`shared/theme/preset.mjs`. Both `desktop/tailwind.config.js` and
`site/tailwind.config.mjs` load it as a Tailwind preset. App-only chrome
(`user-select: none`, scrollbar styling, `--ownbase-mono`) stays in
`desktop/src/index.css` and must not move into the shared layer.

Brand mark and wordmark are copied into `src/assets/` from
`desktop/src-tauri/icon-mark.png` and `desktop/src/assets/wordmark.png`.
Regenerate both together if the lockup changes.

## Deploy

**Local (usual while iterating):**

```bash
make site-publish
```

Uses AWS profile `personal` and bucket `ownbase-ai` by default.

**CI:** pushes to `main` that touch `site/**` or `shared/**` run
[`.github/workflows/site.yml`](../.github/workflows/site.yml), which builds
and uploads via
[`.github/scripts/publish-site-bucket.sh`](../.github/scripts/publish-site-bucket.sh).

Secrets (same shape as the releases bucket):

- `SITE_BUCKET_NAME`
- `SITE_BUCKET_ACCESS_KEY_ID`
- `SITE_BUCKET_SECRET_ACCESS_KEY`
- `SITE_BUCKET_ENDPOINT` (optional, for S3-compatible origins)

Hashed assets under `_astro/` get `max-age=31536000, immutable`. HTML,
`llms.txt`, and favicons get `max-age=60`. No CloudFront invalidation —
a push is live within about a minute. Live stack: CloudFront `EJ8SF4KCUFWJL`
→ S3 `ownbase-ai`, aliases `ownbase.ai` / `www.ownbase.ai`.

## Conventions

- Nav items (How It Works, Agent Harness, Docs, GitHub) are inert until
  destinations exist. Do not invent URLs.
- Every color goes through a token. No raw hex in components except the
  dark "Built for AI" band, which is a deliberate inversion.
- Icons come from `lucide-static` via `Icon.astro`. Brand marks in the
  "Works with" strip come from `simple-icons` where a slug exists.
- `llms.txt` is derived from `README.md` and `INSTALL.md`. Keep it honest
  about which two steps only a human can do (vault password, create the
  server).
