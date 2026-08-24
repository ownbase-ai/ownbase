#!/usr/bin/env bash
# Publish the built marketing site (site/dist) to the ownbase.ai origin.
# Mirrors publish-daemon-bucket.sh conventions: optional S3-compatible
# endpoint, silent-skip left to the caller, short TTL on HTML, immutable
# on hashed assets. No CloudFront invalidation — freshness is TTL.
#
# Env:
#   SITE_BUCKET                    — bucket name
#   AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
#   SITE_BUCKET_ENDPOINT           — optional S3-compatible endpoint
#   SITE_DIST                      — path to the built site (default: site/dist)
set -euo pipefail

SITE_BUCKET="${SITE_BUCKET:?SITE_BUCKET is required}"
SITE_DIST="${SITE_DIST:-site/dist}"

if [ ! -d "$SITE_DIST" ]; then
  echo "site dist not found: $SITE_DIST (run npm run build in site/ first)" >&2
  exit 1
fi

endpoint_flag=()
if [ -n "${SITE_BUCKET_ENDPOINT:-}" ]; then
  endpoint_flag=(--endpoint-url "$SITE_BUCKET_ENDPOINT")
fi

# Pass 1: hashed assets under _astro/ are content-addressed — long cache,
# no --delete (a concurrent deploy of a new HTML that still references an
# old asset mid-swap would 404).
echo "==> s3://$SITE_BUCKET/_astro/ (immutable)"
aws s3 sync "$SITE_DIST/_astro/" "s3://${SITE_BUCKET}/_astro/" \
  --cache-control "public, max-age=31536000, immutable" \
  "${endpoint_flag[@]}"

# Pass 2: everything else (HTML, llms.txt, favicons, og.png, manifest).
# Short TTL so a push is live within a minute. --delete drops retired
# top-level objects; _astro/ is excluded so pass 1's immutables stay.
echo "==> s3://$SITE_BUCKET/ (html + static, max-age=60)"
aws s3 sync "$SITE_DIST/" "s3://${SITE_BUCKET}/" \
  --delete \
  --exclude "_astro/*" \
  --cache-control "public, max-age=60" \
  "${endpoint_flag[@]}"

# llms.txt is the primary CTA — force the content-type browsers and agents
# expect. aws s3 sync's auto-guess is usually right, but be explicit.
if [ -f "$SITE_DIST/llms.txt" ]; then
  echo "==> s3://$SITE_BUCKET/llms.txt (text/plain)"
  aws s3 cp "$SITE_DIST/llms.txt" "s3://${SITE_BUCKET}/llms.txt" \
    --content-type "text/plain; charset=utf-8" \
    --cache-control "public, max-age=60" \
    "${endpoint_flag[@]}"
fi

echo "==> Published site/dist to s3://$SITE_BUCKET"
