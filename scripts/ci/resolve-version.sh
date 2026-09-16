#!/usr/bin/env bash
# Resolve SemVer for CX builds.
# Source of truth: git tags vX.Y.Z (or X.Y.Z), with .localbuild as a floor.
# On main (BUMP=1): bump point|minor|major → image tag vX.Y.Z-<sha> (+ latest).
# If HEAD is already an exact vX.Y.Z tag (e.g. commitme.sh), use it (no bump).
# On branches: no bump; emit vX.Y.Z-<sha> from current base.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"

BUMP="${BUMP:-0}"
BUMP_KIND="${BUMP_KIND:-point}"
SHA="${SHA:-$(git rev-parse --short HEAD)}"
LOCALBUILD_FILE="${ROOT}/.localbuild"

latest_semver_base() {
  local tag from_tags="0.0.0" from_file=""
  tag="$(git tag -l 'v*.*.*' '*.*.*' 2>/dev/null | grep -E '^v?[0-9]+\.[0-9]+\.[0-9]+$' | sed 's/^v//' | sort -V | tail -n 1 || true)"
  if [[ -n "${tag}" ]]; then
    from_tags="${tag}"
  fi
  if [[ -f "${LOCALBUILD_FILE}" ]]; then
    from_file="$(tr -d '[:space:]' < "${LOCALBUILD_FILE}")"
    from_file="${from_file#v}"
  fi
  if [[ -n "${from_file}" ]] && [[ "$(printf '%s\n%s\n' "${from_tags}" "${from_file}" | sort -V | tail -n 1)" == "${from_file}" ]]; then
    echo "${from_file}"
  else
    echo "${from_tags}"
  fi
}

parse_bump_from_commit() {
  local msg
  msg="$(git log -1 --pretty=%B 2>/dev/null || true)"
  if echo "${msg}" | grep -qiE '\[bump[[:space:]]+major\]'; then
    echo "major"
  elif echo "${msg}" | grep -qiE '\[bump[[:space:]]+minor\]'; then
    echo "minor"
  elif echo "${msg}" | grep -qiE '\[bump[[:space:]]+point\]|\[bump[[:space:]]+patch\]'; then
    echo "point"
  else
    echo ""
  fi
}

HEAD_TAG="$(git describe --tags --exact-match HEAD 2>/dev/null || true)"
HEAD_SEMVER=""
if [[ "${HEAD_TAG}" =~ ^v?([0-9]+\.[0-9]+\.[0-9]+)$ ]]; then
  HEAD_SEMVER="${BASH_REMATCH[1]}"
fi

if [[ -n "${HEAD_SEMVER}" ]]; then
  SEMVER="${HEAD_SEMVER}"
  VERSION_TAG="v${SEMVER}-${SHA}"
  GIT_TAG="v${SEMVER}"
  CREATE_GIT_TAG=false
  echo "base=${SEMVER} (HEAD already tagged)"
  echo "bump=0 kind=none"
  echo "semver=${SEMVER}"
  echo "version_tag=${VERSION_TAG}"
  echo "git_tag=${GIT_TAG}"
  echo "create_git_tag=false"
  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    {
      echo "semver=${SEMVER}"
      echo "version_tag=${VERSION_TAG}"
      echo "git_tag=${GIT_TAG}"
      echo "base=${SEMVER}"
      echo "create_git_tag=false"
    } >> "${GITHUB_OUTPUT}"
  fi
  exit 0
fi

BASE="$(latest_semver_base)"
IFS='.' read -r MAJOR MINOR PATCH <<<"${BASE}"
MAJOR=$((10#${MAJOR:-0}))
MINOR=$((10#${MINOR:-0}))
PATCH=$((10#${PATCH:-0}))

COMMIT_BUMP="$(parse_bump_from_commit)"
if [[ -n "${COMMIT_BUMP}" ]]; then
  BUMP_KIND="${COMMIT_BUMP}"
fi

SEMVER="${MAJOR}.${MINOR}.${PATCH}"
CREATE_GIT_TAG=false
if [[ "${BUMP}" == "1" || "${BUMP}" == "true" ]]; then
  case "${BUMP_KIND}" in
    major) MAJOR=$((MAJOR + 1)); MINOR=0; PATCH=0 ;;
    minor) MINOR=$((MINOR + 1)); PATCH=0 ;;
    point|patch) PATCH=$((PATCH + 1)) ;;
    *)
      echo "ERROR: invalid BUMP_KIND=${BUMP_KIND} (want point|minor|major)" >&2
      exit 1
      ;;
  esac
  SEMVER="${MAJOR}.${MINOR}.${PATCH}"
  CREATE_GIT_TAG=true
fi

VERSION_TAG="v${SEMVER}-${SHA}"
GIT_TAG="v${SEMVER}"

echo "base=${BASE}"
echo "bump=${BUMP} kind=${BUMP_KIND}"
echo "semver=${SEMVER}"
echo "version_tag=${VERSION_TAG}"
echo "git_tag=${GIT_TAG}"
echo "create_git_tag=${CREATE_GIT_TAG}"

if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  {
    echo "semver=${SEMVER}"
    echo "version_tag=${VERSION_TAG}"
    echo "git_tag=${GIT_TAG}"
    echo "base=${BASE}"
    echo "create_git_tag=${CREATE_GIT_TAG}"
  } >> "${GITHUB_OUTPUT}"
fi
