#!/usr/bin/env bash
# Publish a per-developer rf2vc preview via GitOps (dasmlab-live-cicd).
set -euo pipefail

VERSION_TAG="${VERSION_TAG:?}"
ACTOR="${PREVIEW_ACTOR:?}"
CLUSTER_APPS="${CLUSTER_APPS_DOMAIN:-apps.2026-prod-1.ocp.dasmlab.org}"

OWNER="$(echo "${ACTOR}" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9]+/-/g; s/^-+//; s/-+$//; s/-+/-/g' | cut -c1-20)"
OWNER="${OWNER:-dev}"
NS="rf2vc-dev-${OWNER}"
HOST="dev-${OWNER}-rf2vc.${CLUSTER_APPS}"
PREVIEW_URL="https://${HOST}"

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
RENDERED="$(mktemp)"
sed \
  -e "s|__VERSION__|${VERSION_TAG}|g" \
  -e "s|__PREVIEW_NS__|${NS}|g" \
  -e "s|__PREVIEW_HOST__|${HOST}|g" \
  -e "s|__PREVIEW_OWNER__|${OWNER}|g" \
  "${ROOT}/k8s_envelope/rf2vc_preview-ocp.yaml" > "${RENDERED}"

echo "Preview owner=${OWNER} ns=${NS} host=${HOST} version=${VERSION_TAG}"

if [[ "${SKIP_PREVIEW_CERT:-}" != "true" ]]; then
  bash "${ROOT}/scripts/ci/ensure-preview-cert.sh" "${HOST}"
fi

DEPLOY_TOKEN=""
if [ -f "/home/dasm/gh_token" ]; then
  DEPLOY_TOKEN="$(tr -d '\n\r' < /home/dasm/gh_token)"
fi
if [ -z "${DEPLOY_TOKEN}" ]; then
  DEPLOY_TOKEN="${DASMLAB_GHCR_PAT:-${GH_TOKEN:-}}"
fi
if [ -z "${DEPLOY_TOKEN}" ]; then
  echo "ERROR: deploy token not set" >&2
  exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}" "${RENDERED}"' EXIT
git clone --depth 1 "https://x-access-token:${DEPLOY_TOKEN}@github.com/lmcdasm/dasmlab-live-cicd.git" "${WORK}/live-cicd"
PREVIEW_DIR="${WORK}/live-cicd/clusters/2026-prod-1/rf2vc/previews"
mkdir -p "${PREVIEW_DIR}"
cp "${RENDERED}" "${PREVIEW_DIR}/${OWNER}.yaml"

cd "${WORK}/live-cicd"
git config user.name "dasmlab-bot"
git config user.email "ci@dasmlab.org"
git add "clusters/2026-prod-1/rf2vc/previews/${OWNER}.yaml"
if git diff --cached --quiet; then
  echo "No GitOps preview changes"
else
  git commit -m "preview(${OWNER}): rf2vc ${VERSION_TAG}"
  git push
fi

echo "PREVIEW_URL=${PREVIEW_URL}"
echo "Preview GitOps published: ${PREVIEW_URL}"
