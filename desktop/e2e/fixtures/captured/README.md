# Captured CLI JSON goldens

Redacted `ownbasectl --json` documents used by `src/lib/captured-shape.test.ts`
to catch field renames hermetic fixtures alone cannot see.

Refresh from a throwaway Base (never a production one):

```bash
ownbasectl vault unlock
make smoke-test                          # or reuse an existing throwaway
cd desktop && npm run e2e:capture -- ownbase-fresh
# review redaction, then commit
```

`capture.mjs` scrubs IPs, hostnames, repo URLs, home paths, and sockets before
write. Still eyeball the diff before committing — this tree is public.
