#!/usr/bin/env bash

set -eu

VERSION_PATTERN='^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$'
VERSION_FILE_PATTERN='^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\n$'
RELEASE_BRANCH_PATTERN='^release/(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$'
VERSION_SENTINEL='__release_version_end__'

die() {
  printf 'release-version: %s\n' "$1" >&2
  exit 1
}

matches() {
  value=$1
  expression=$2

  case "$value" in
    *'
'*) return 1 ;;
  esac
  printf '%s\n' "$value" | grep -Eq "$expression"
}

read_base_release_line() {
  base_ref=$1
  matches "$base_ref" "$RELEASE_BRANCH_PATTERN" || die "invalid release branch: $base_ref"
  release_line=${base_ref#release/}
  BASE_MAJOR=${release_line%%.*}
  BASE_MINOR=${release_line#*.}
}

read_release_line() {
  base_ref=$1
  version=$2

  read_base_release_line "$base_ref"
  matches "$version" "$VERSION_PATTERN" || die "invalid version: $version"
  VERSION_MAJOR=${version%%.*}
  version_remainder=${version#*.}
  VERSION_MINOR=${version_remainder%%.*}
  VERSION_PATCH=${version_remainder#*.}
  if [ "$VERSION_MAJOR" != "$BASE_MAJOR" ] || [ "$VERSION_MINOR" != "$BASE_MINOR" ]; then
    die "version $version does not match release branch $base_ref"
  fi
}

next_patch() {
  major=$1
  minor=$2
  remote=$3
  max_patch=-1
  tag_patches=''
  peeled_patches=''

  remote_tags=$(git ls-remote --tags "$remote" "refs/tags/v${major}.${minor}.*") || die "cannot read tags from remote $remote"
  while read -r _ tag_ref; do
    [ -n "${tag_ref:-}" ] || continue
    tag_prefix="refs/tags/v${major}.${minor}."
    case "$tag_ref" in
      "$tag_prefix"*'^{}')
        tag_patch=${tag_ref#"$tag_prefix"}
        tag_patch=${tag_patch%'^{}'}
        matches "$tag_patch" '^(0|[1-9][0-9]{0,8})$' || die "invalid remote tag: ${tag_ref#refs/tags/}"
        case " $peeled_patches " in *" $tag_patch "*) die "duplicate peeled remote tag: v${major}.${minor}.${tag_patch}" ;; esac
        peeled_patches="$peeled_patches $tag_patch"
        continue
        ;;
      "$tag_prefix"*) ;;
      *) continue ;;
    esac
    tag_patch=${tag_ref#"$tag_prefix"}
    matches "$tag_patch" '^(0|[1-9][0-9]{0,8})$' || die "invalid remote tag: ${tag_ref#refs/tags/}"
    case " $tag_patches " in *" $tag_patch "*) die "duplicate remote tag: v${major}.${minor}.${tag_patch}" ;; esac
    tag_patches="$tag_patches $tag_patch"
    if [ "$tag_patch" -gt "$max_patch" ]; then max_patch=$tag_patch; fi
  done <<EOF
$remote_tags
EOF

  for tag_patch in $tag_patches; do
    case " $peeled_patches " in *" $tag_patch "*) ;; *) die "remote tag v${major}.${minor}.${tag_patch} is lightweight" ;; esac
  done
  for tag_patch in $peeled_patches; do
    case " $tag_patches " in *" $tag_patch "*) ;; *) die "missing remote tag object: v${major}.${minor}.${tag_patch}" ;; esac
  done
  if [ "$max_patch" -lt 0 ]; then
    printf '0\n'
    return
  fi
  [ "$max_patch" -lt 999999999 ] || die "release patch limit reached: v${major}.${minor}.${max_patch}"
  expected_patch=0
  while [ "$expected_patch" -le "$max_patch" ]; do
    case " $tag_patches " in *" $expected_patch "*) ;; *) die "missing remote tag: v${major}.${minor}.${expected_patch}" ;; esac
    expected_patch=$((expected_patch + 1))
  done
  printf '%s\n' "$((max_patch + 1))"
}

validate_pr() {
  [ "$#" -eq 4 ] || die 'usage: validate-pr <base> <head> <version> <base-version>'
  base_ref=$1
  head_ref=$2
  version=$3
  base_version=$4

  read_release_line "$base_ref" "$version"
  matches "$base_version" "$VERSION_PATTERN" || die "invalid base version: $base_version"
  [ "$version" != "$base_version" ] || die "version $version is unchanged from base version"
  matches "$head_ref" '^(feature|fix|refactor|chore|docs|test)-[a-z0-9]+(-[a-z0-9]+)*$' || die "invalid task branch: $head_ref"
  expected_patch=$(next_patch "$BASE_MAJOR" "$BASE_MINOR" origin)
  [ "$VERSION_PATCH" -eq "$expected_patch" ] || die "version $version is not the next release version; expected ${BASE_MAJOR}.${BASE_MINOR}.${expected_patch}"
  printf 'release version %s is valid for %s from %s\n' "$version" "$base_ref" "$head_ref"
}

inspect_tag() {
  tag_name=$1
  remote=$2
  TAG_OBJECT=''
  TAG_TARGET=''
  exact_tag_lines=$(git ls-remote --tags "$remote" "refs/tags/$tag_name" "refs/tags/$tag_name^{}") || die "cannot inspect $tag_name on remote $remote"
  while read -r object_sha tag_ref; do
    [ -n "${tag_ref:-}" ] || continue
    case "$tag_ref" in
      "refs/tags/$tag_name") TAG_OBJECT=$object_sha ;;
      "refs/tags/$tag_name^{}") TAG_TARGET=$object_sha ;;
    esac
  done <<EOF
$exact_tag_lines
EOF
}

verify_existing_tag() {
  tag_name=$1
  requested_sha=$2
  [ -n "$TAG_OBJECT" ] || return 1
  [ -n "$TAG_TARGET" ] || die "remote tag $tag_name is lightweight"
  [ "$TAG_TARGET" = "$requested_sha" ] || die "remote tag $tag_name points to $TAG_TARGET, not $requested_sha"
  return 0
}

create_tag() {
  [ "$#" -eq 4 ] || die 'usage: create-tag <base> <version> <sha> <remote>'
  base_ref=$1
  version=$2
  requested_ref=$3
  remote=$4
  read_release_line "$base_ref" "$version"
  requested_sha=$(git rev-parse --verify "${requested_ref}^{commit}") || die "invalid commit: $requested_ref"
  tag_name="v$version"
  expected_patch=$(next_patch "$BASE_MAJOR" "$BASE_MINOR" "$remote")
  inspect_tag "$tag_name" "$remote"
  if verify_existing_tag "$tag_name" "$requested_sha"; then
    printf 'annotated tag %s already points to %s\n' "$tag_name" "$requested_sha"
    return
  fi
  [ "$VERSION_PATCH" -eq "$expected_patch" ] || die "version $version is not the next release version; expected ${BASE_MAJOR}.${BASE_MINOR}.${expected_patch}"
  git tag -a "$tag_name" "$requested_sha" -m "Release $tag_name"
  git push "$remote" "refs/tags/$tag_name:refs/tags/$tag_name"
  printf 'created annotated tag %s at %s\n' "$tag_name" "$requested_sha"
}

read_version_file() {
  [ "$#" -eq 1 ] || die 'usage: read-version <git-path>'
  version_path=$1
  version_ref=${version_path%%:*}
  git rev-parse --verify "${version_ref}^{commit}" >/dev/null 2>&1 || return 4
  git cat-file -e "$version_path" 2>/dev/null || return 2
  VERSION_WITH_SENTINEL=$(set -o pipefail; git show "$version_path" | VERSION_FILE_PATTERN="$VERSION_FILE_PATTERN" VERSION_SENTINEL="$VERSION_SENTINEL" node -e "const fs = require('fs'); const value = fs.readFileSync(0); const source = value.toString('utf8'); if (!Buffer.from(source, 'utf8').equals(value) || value.length > 30 || !new RegExp(process.env.VERSION_FILE_PATTERN).test(source)) process.exit(1); process.stdout.write(source.slice(0, -1) + process.env.VERSION_SENTINEL)") || return 3
  printf '%s\n' "$VERSION_WITH_SENTINEL"
}

read_version_or_default() {
  [ "$#" -eq 2 ] || die 'usage: read-version-or-default <git-path> <default-version>'
  version_path=$1
  default_version=$2
  matches "$default_version" "$VERSION_PATTERN" || die "invalid default version: $default_version"

  if version_with_sentinel=$(read_version_file "$version_path"); then
    printf '%s\n' "$version_with_sentinel"
    return
  else
    read_status=$?
  fi
  [ "$read_status" -eq 2 ] || die "invalid VERSION from $version_path"
  printf '%s%s\n' "$default_version" "$VERSION_SENTINEL"
}

read_commit_version() {
  commit=$1
  HISTORY_VERSION=''
  if HISTORY_VERSION_WITH_SENTINEL=$(read_version_file "${commit}:VERSION"); then
    HISTORY_VERSION_STATUS='found'
  else
    read_status=$?
    if [ "$read_status" -eq 2 ]; then HISTORY_VERSION_STATUS='missing'; return; fi
    die "invalid VERSION at $commit"
  fi
  case "$HISTORY_VERSION_WITH_SENTINEL" in *"$VERSION_SENTINEL") ;; *) die "invalid VERSION at $commit" ;; esac
  HISTORY_VERSION=${HISTORY_VERSION_WITH_SENTINEL%"$VERSION_SENTINEL"}
}

read_history_release_version() {
  history_version=$1
  matches "$history_version" "$VERSION_PATTERN" || die "invalid VERSION: $history_version"
  HISTORY_MAJOR=${history_version%%.*}
  history_remainder=${history_version#*.}
  HISTORY_MINOR=${history_remainder%%.*}
  HISTORY_PATCH=${history_remainder#*.}
  [ "$HISTORY_MAJOR" = "$BASE_MAJOR" ] && [ "$HISTORY_MINOR" = "$BASE_MINOR" ]
}

validate_reconciliation_history() {
  target_sha=$1
  history_patch=-1
  while read -r history_sha; do
    [ -n "${history_sha:-}" ] || continue
    read_commit_version "$history_sha"
    if [ "$HISTORY_VERSION_STATUS" = missing ]; then
      [ "$history_patch" -lt 0 ] || die "VERSION is missing after release history starts at $history_sha"
      continue
    fi
    history_version=$HISTORY_VERSION
    if read_history_release_version "$history_version"; then
      if [ "$history_patch" -lt 0 ]; then
        [ "$HISTORY_PATCH" -eq 0 ] || die "release history starts at ${BASE_MAJOR}.${BASE_MINOR}.${HISTORY_PATCH}, not ${BASE_MAJOR}.${BASE_MINOR}.0"
      elif [ "$HISTORY_PATCH" -lt "$history_patch" ]; then
        die "release history moves backward from ${BASE_MAJOR}.${BASE_MINOR}.${history_patch} to ${BASE_MAJOR}.${BASE_MINOR}.${HISTORY_PATCH}"
      elif [ "$HISTORY_PATCH" -gt "$history_patch" ] && [ "$HISTORY_PATCH" -ne "$((history_patch + 1))" ]; then
        die "release history skips from ${BASE_MAJOR}.${BASE_MINOR}.${history_patch} to ${BASE_MAJOR}.${BASE_MINOR}.${HISTORY_PATCH}"
      fi
      history_patch=$HISTORY_PATCH
    elif [ "$history_patch" -ge 0 ]; then
      die "version $history_version does not match release branch release/${BASE_MAJOR}.${BASE_MINOR}"
    fi
  done <<EOF
$(git rev-list --first-parent --reverse "$target_sha")
EOF
  [ "$history_patch" -ge 0 ] || die "no ${BASE_MAJOR}.${BASE_MINOR} release versions found before $target_sha"
}

preflight_reconciliation_tags() {
  base_ref=$1
  remote=$2
  expected_patch=$(next_patch "$BASE_MAJOR" "$BASE_MINOR" "$remote")
  planned_next_patch=$expected_patch
  while read -r planned_version planned_sha; do
    [ -n "${planned_version:-}" ] || continue
    inspect_tag "v$planned_version" "$remote"
    if verify_existing_tag "v$planned_version" "$planned_sha"; then continue; fi
    planned_patch=${planned_version##*.}
    [ "$planned_patch" -eq "$planned_next_patch" ] || die "version $planned_version is not the next release version; expected ${BASE_MAJOR}.${BASE_MINOR}.${planned_next_patch}"
    planned_next_patch=$((planned_next_patch + 1))
  done <<EOF
$RECONCILIATION_PLAN
EOF
}

reconcile_tags() {
  [ "$#" -eq 3 ] || die 'usage: reconcile-tags <base> <sha> <remote>'
  base_ref=$1
  requested_ref=$2
  remote=$3
  read_base_release_line "$base_ref"
  requested_sha=$(git rev-parse --verify "${requested_ref}^{commit}") || die "invalid commit: $requested_ref"
  validate_reconciliation_history "$requested_sha"

  reconciled_patch=-1
  reconciled_sha=''
  RECONCILIATION_PLAN=''
  while read -r history_sha; do
    [ -n "${history_sha:-}" ] || continue
    read_commit_version "$history_sha"
    [ "$HISTORY_VERSION_STATUS" = missing ] && continue
    read_history_release_version "$HISTORY_VERSION" || continue
    if [ "$HISTORY_PATCH" -ne "$reconciled_patch" ]; then
      if [ "$reconciled_patch" -ge 0 ]; then
        RECONCILIATION_PLAN="${RECONCILIATION_PLAN}${BASE_MAJOR}.${BASE_MINOR}.${reconciled_patch} ${reconciled_sha}
"
      fi
      reconciled_patch=$HISTORY_PATCH
    fi
    reconciled_sha=$history_sha
  done <<EOF
$(git rev-list --first-parent --reverse "$requested_sha")
EOF
  RECONCILIATION_PLAN="${RECONCILIATION_PLAN}${BASE_MAJOR}.${BASE_MINOR}.${reconciled_patch} ${reconciled_sha}
"
  preflight_reconciliation_tags "$base_ref" "$remote"
  while read -r planned_version planned_sha; do
    [ -n "${planned_version:-}" ] || continue
    create_tag "$base_ref" "$planned_version" "$planned_sha" "$remote"
  done <<EOF
$RECONCILIATION_PLAN
EOF
  printf 'reconciled release tags through %s\n' "$requested_sha"
}

[ "$#" -gt 0 ] || die 'usage: release-version.sh <validate-pr|create-tag|reconcile-tags|read-version|read-version-or-default> ...'
command_name=$1
shift
case "$command_name" in
  validate-pr) validate_pr "$@" ;;
  create-tag) create_tag "$@" ;;
  reconcile-tags) reconcile_tags "$@" ;;
  read-version)
    read_version_file "$@" || { read_status=$?; [ "$read_status" -eq 2 ] && die "cannot read VERSION from ${1:-}"; die "invalid VERSION from ${1:-}"; }
    ;;
  read-version-or-default) read_version_or_default "$@" ;;
  *) die "unknown command: $command_name" ;;
esac
