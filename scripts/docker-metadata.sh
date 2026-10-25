#!/usr/bin/env bash
set -euo pipefail

# Used by the reusable GHCR workflow. Never publish a PR or an arbitrary branch.
: "${GITHUB_REPOSITORY:?}"
: "${GITHUB_REF:?}"
: "${GITHUB_SHA:?}"
: "${GITHUB_OUTPUT:?}"
[[ "$GITHUB_REPOSITORY" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || { echo 'Invalid repository' >&2; exit 1; }
[[ "$GITHUB_SHA" =~ ^[a-f0-9]{40}$ ]] || { echo 'Invalid commit SHA' >&2; exit 1; }
image="ghcr.io/$(printf '%s' "$GITHUB_REPOSITORY" | tr '[:upper:]' '[:lower:]')"
source_version="$(cat cmd/agent/VERSION)"

if [[ "$GITHUB_REF" == refs/heads/main ]]; then
  primary_tag=edge
  version="${source_version}-edge.${GITHUB_SHA:0:12}"
  tags=$(printf '%s\n' "${image}:edge" "${image}:sha-${GITHUB_SHA:0:12}")
elif [[ "$GITHUB_REF" =~ ^refs/tags/(v[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z][0-9A-Za-z.-]*)?)$ ]]; then
  version="${GITHUB_REF#refs/tags/}"
  [[ ${#version} -le 128 && "$version" == "$source_version" ]] || {
    echo 'Release tag must match cmd/agent/VERSION and fit a Docker tag' >&2
    exit 1
  }
  primary_tag="$version"
  tags="${image}:${version}"
  if [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    tags=$(printf '%s\n' "$tags" "${image}:latest")
  fi
else
  echo 'Only main or a v* release tag may publish images' >&2
  exit 1
fi

{
  printf 'image=%s\nversion=%s\nprimary_tag=%s\n' "$image" "$version" "$primary_tag"
  printf 'tags<<IMAGES\n%s\nIMAGES\n' "$tags"
} >> "$GITHUB_OUTPUT"
