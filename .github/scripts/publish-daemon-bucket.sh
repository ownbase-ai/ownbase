#!/usr/bin/env bash
# Publish signed daemon binaries + latest.json from a GitHub Release to
# releases.ownbase.ai. Used by the release workflow and by finish-release
# when GoReleaser succeeded but a later step (e.g. Homebrew) aborted the job.
#
# Env:
#   TAG                            — e.g. v0.5.0
#   RELEASES_BUCKET                — bucket name
#   AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
#   RELEASES_BUCKET_ENDPOINT       — optional S3-compatible endpoint
#   GH_TOKEN                       — read the release assets
set -euo pipefail

TAG="${TAG:?TAG is required (e.g. v0.5.0)}"
RELEASES_BUCKET="${RELEASES_BUCKET:?RELEASES_BUCKET is required}"

if [[ ! "$TAG" =~ ^v[0-9] ]]; then
  echo "TAG must look like vX.Y.Z, got: $TAG" >&2
  exit 1
fi

endpoint_flag=()
if [ -n "${RELEASES_BUCKET_ENDPOINT:-}" ]; then
  endpoint_flag=(--endpoint-url "$RELEASES_BUCKET_ENDPOINT")
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

echo "==> Downloading daemon assets from release $TAG"
gh release download "$TAG" \
  --repo ownbase-ai/ownbase \
  --dir "$work" \
  --pattern "ownbased-linux-amd64" \
  --pattern "ownbased-linux-amd64.minisig" \
  --pattern "ownbased-linux-arm64" \
  --pattern "ownbased-linux-arm64.minisig"

for arch in amd64 arm64; do
  for f in "ownbased-linux-${arch}" "ownbased-linux-${arch}.minisig"; do
    src="$work/$f"
    if [ ! -f "$src" ]; then
      echo "missing release asset: $f" >&2
      ls -la "$work" >&2 || true
      exit 1
    fi
    echo "==> s3://$RELEASES_BUCKET/daemon/${TAG}/$f"
    aws s3 cp "$src" "s3://${RELEASES_BUCKET}/daemon/${TAG}/${f}" "${endpoint_flag[@]}"
    aws s3 cp "$src" "s3://${RELEASES_BUCKET}/daemon/latest/${f}" "${endpoint_flag[@]}"
  done
done

released_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
jq -n \
  --arg v "$TAG" \
  --arg at "$released_at" \
  '{
    schema: 1,
    released_at: $at,
    components: {
      cli:    {version: $v},
      app:    {version: $v},
      daemon: {version: $v}
    }
  }' > "$work/latest.json"

echo "==> s3://$RELEASES_BUCKET/latest.json"
aws s3 cp "$work/latest.json" "s3://${RELEASES_BUCKET}/latest.json" \
  --content-type application/json \
  --cache-control "public, max-age=300" \
  "${endpoint_flag[@]}"

echo "==> Published $TAG to releases.ownbase.ai"
