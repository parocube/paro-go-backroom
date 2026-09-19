#!/usr/bin/env bash

set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
RELEASE_SCRIPT="$SCRIPT_DIR/release-version.sh"
TEST_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/member-release-version-test.XXXXXX")
TEST_NUMBER=0
VERSION_SENTINEL='__release_version_end__'

cleanup() {
  rm -rf "$TEST_ROOT"
}

trap cleanup EXIT HUP INT TERM

fail() {
  printf 'not ok %s - %s\n' "$((TEST_NUMBER + 1))" "$1" >&2
  exit 1
}

pass() {
  TEST_NUMBER=$((TEST_NUMBER + 1))
  printf 'ok %s - %s\n' "$TEST_NUMBER" "$1"
}

new_repository() {
  repository_name=$1
  REPOSITORY_ROOT="$TEST_ROOT/$repository_name"
  WORK_REPOSITORY="$REPOSITORY_ROOT/work"
  BARE_ORIGIN="$REPOSITORY_ROOT/origin.git"

  mkdir -p "$REPOSITORY_ROOT"
  git init --bare -q "$BARE_ORIGIN"
  git init -q "$WORK_REPOSITORY"
  git -C "$WORK_REPOSITORY" config user.name 'Release Test'
  git -C "$WORK_REPOSITORY" config user.email 'release-test@example.invalid'
  git -C "$WORK_REPOSITORY" commit --allow-empty -q -m 'first'
  FIRST_SHA=$(git -C "$WORK_REPOSITORY" rev-parse HEAD)
  git -C "$WORK_REPOSITORY" commit --allow-empty -q -m 'second'
  SECOND_SHA=$(git -C "$WORK_REPOSITORY" rev-parse HEAD)
  PRIMARY_BRANCH=$(git -C "$WORK_REPOSITORY" branch --show-current)
  git -C "$WORK_REPOSITORY" remote add origin "$BARE_ORIGIN"
  git -C "$WORK_REPOSITORY" push -q origin HEAD:refs/heads/release/1.0
}

run_release_script() {
  (
    cd "$WORK_REPOSITORY"
    bash "$RELEASE_SCRIPT" "$@"
  )
}

remote_tag_object() {
  git ls-remote "$BARE_ORIGIN" "refs/tags/$1" | awk 'NR == 1 { print $1 }'
}

remote_tag_target() {
  git ls-remote "$BARE_ORIGIN" "refs/tags/$1^{}" | awk 'NR == 1 { print $1 }'
}

commit_version() {
  version=$1
  printf '%s\n' "$version" > "$WORK_REPOSITORY/VERSION"
  git -C "$WORK_REPOSITORY" add VERSION
  git -C "$WORK_REPOSITORY" commit --allow-empty -q -m "Release $version"
  VERSION_SHA=$(git -C "$WORK_REPOSITORY" rev-parse HEAD)
}

commit_raw_version() {
  version_bytes=$1
  printf '%b' "$version_bytes" > "$WORK_REPOSITORY/VERSION"
  git -C "$WORK_REPOSITORY" add VERSION
  git -C "$WORK_REPOSITORY" commit --allow-empty -q -m 'Update VERSION bytes'
  VERSION_SHA=$(git -C "$WORK_REPOSITORY" rev-parse HEAD)
}

new_repository validate

case_name='validate-pr accepts the bootstrap release with missing base VERSION represented as 0.0.0'
if run_release_script validate-pr release/1.0 chore-release-automation-bootstrap 1.0.0 0.0.0; then pass "$case_name"; else fail "$case_name"; fi

case_name='validate-pr rejects slash-style task branches'
if run_release_script validate-pr release/1.0 feature/release-automation 1.0.0 0.0.0; then fail "$case_name"; else pass "$case_name"; fi

case_name='validate-pr rejects a version outside the base release line'
if run_release_script validate-pr release/1.0 chore-release-automation 1.1.0 1.0.0; then fail "$case_name"; else pass "$case_name"; fi

git -C "$WORK_REPOSITORY" tag -a v1.0.0 "$FIRST_SHA" -m 'Release v1.0.0'
git -C "$WORK_REPOSITORY" push -q origin refs/tags/v1.0.0:refs/tags/v1.0.0

case_name='validate-pr rejects a skipped patch version'
if run_release_script validate-pr release/1.0 chore-release-automation 1.0.2 1.0.0; then fail "$case_name"; else pass "$case_name"; fi

case_name='validate-pr accepts the next patch version'
if run_release_script validate-pr release/1.0 chore-release-automation 1.0.1 1.0.0; then pass "$case_name"; else fail "$case_name"; fi

new_repository unchanged-version
case_name='validate-pr rejects an unchanged base version'
if run_release_script validate-pr release/1.0 chore-release-automation 1.0.0 1.0.0; then fail "$case_name"; else pass "$case_name"; fi

new_repository gapped-history
git -C "$WORK_REPOSITORY" tag -a v1.0.0 "$FIRST_SHA" -m 'Release v1.0.0'
git -C "$WORK_REPOSITORY" tag -a v1.0.2 "$SECOND_SHA" -m 'Release v1.0.2'
git -C "$WORK_REPOSITORY" push -q origin refs/tags/v1.0.0:refs/tags/v1.0.0 refs/tags/v1.0.2:refs/tags/v1.0.2
case_name='validate-pr rejects gapped annotated release tags'
if run_release_script validate-pr release/1.0 chore-release-automation 1.0.3 1.0.2; then fail "$case_name"; else pass "$case_name"; fi

new_repository lightweight-history
git -C "$WORK_REPOSITORY" tag v1.0.0 "$FIRST_SHA"
git -C "$WORK_REPOSITORY" push -q origin refs/tags/v1.0.0:refs/tags/v1.0.0
case_name='validate-pr rejects a lightweight preceding release tag'
if run_release_script validate-pr release/1.0 chore-release-automation 1.0.1 1.0.0; then fail "$case_name"; else pass "$case_name"; fi

new_repository contiguous-history
git -C "$WORK_REPOSITORY" tag -a v1.0.0 "$FIRST_SHA" -m 'Release v1.0.0'
git -C "$WORK_REPOSITORY" tag -a v1.0.1 "$SECOND_SHA" -m 'Release v1.0.1'
git -C "$WORK_REPOSITORY" push -q origin refs/tags/v1.0.0:refs/tags/v1.0.0 refs/tags/v1.0.1:refs/tags/v1.0.1
case_name='validate-pr accepts the next version after contiguous annotated tags'
if run_release_script validate-pr release/1.0 chore-release-automation 1.0.2 1.0.1; then pass "$case_name"; else fail "$case_name"; fi

new_repository reconcile
commit_version 0.9.9
commit_version 1.0.0
RECONCILE_ZERO_SHA=$VERSION_SHA
commit_version 1.0.0
RECONCILE_DUPLICATE_ZERO_SHA=$VERSION_SHA
commit_version 1.0.1
RECONCILE_ONE_SHA=$VERSION_SHA
git -C "$WORK_REPOSITORY" branch reconcile-merge
git -C "$WORK_REPOSITORY" checkout -q reconcile-merge
commit_version 1.0.2
git -C "$WORK_REPOSITORY" checkout -q "$PRIMARY_BRANCH"
git -C "$WORK_REPOSITORY" merge --no-ff -q reconcile-merge -m 'Merge Release 1.0.2'
RECONCILE_TWO_SHA=$(git -C "$WORK_REPOSITORY" rev-parse HEAD)
git -C "$WORK_REPOSITORY" push -q origin HEAD:refs/heads/release/1.0
case_name='reconcile-tags maps rebase squash and merge release versions to final first-parent commits'
if ! run_release_script reconcile-tags release/1.0 "$RECONCILE_TWO_SHA" origin; then fail "$case_name"; fi
if [ "$(remote_tag_target v1.0.0)" != "$RECONCILE_DUPLICATE_ZERO_SHA" ] || [ "$(remote_tag_target v1.0.1)" != "$RECONCILE_ONE_SHA" ] || [ "$(remote_tag_target v1.0.2)" != "$RECONCILE_TWO_SHA" ] || [ -n "$(remote_tag_object v0.9.9)" ]; then fail "$case_name"; fi
pass "$case_name"
case_name='reconcile-tags accepts earlier events after a later event backfills tags'
if ! run_release_script reconcile-tags release/1.0 "$RECONCILE_ONE_SHA" origin || ! run_release_script reconcile-tags release/1.0 "$RECONCILE_DUPLICATE_ZERO_SHA" origin; then fail "$case_name"; fi
pass "$case_name"

new_repository missing-history
commit_version 0.9.9
MIGRATION_SHA=$VERSION_SHA
commit_version 1.0.0
MISSING_ZERO_SHA=$VERSION_SHA
git -C "$WORK_REPOSITORY" rm -q VERSION
git -C "$WORK_REPOSITORY" commit -q -m 'Remove version unexpectedly'
MISSING_AFTER_RELEASE_SHA=$(git -C "$WORK_REPOSITORY" rev-parse HEAD)
git -C "$WORK_REPOSITORY" push -q origin HEAD:refs/heads/release/1.0
case_name='reconcile-tags rejects a missing VERSION after release history starts without writing tags'
if run_release_script reconcile-tags release/1.0 "$MISSING_AFTER_RELEASE_SHA" origin || [ -n "$(git ls-remote --tags "$BARE_ORIGIN")" ]; then fail "$case_name"; else pass "$case_name"; fi

new_repository reconcile-skipped
commit_version 1.0.0
commit_version 1.0.2
RECONCILE_SKIPPED_SHA=$VERSION_SHA
git -C "$WORK_REPOSITORY" push -q origin HEAD:refs/heads/release/1.0
case_name='reconcile-tags rejects skipped versions without creating tags'
if run_release_script reconcile-tags release/1.0 "$RECONCILE_SKIPPED_SHA" origin || [ -n "$(git ls-remote --tags "$BARE_ORIGIN")" ]; then fail "$case_name"; else pass "$case_name"; fi

new_repository reconcile-backward
commit_version 1.0.0
commit_version 1.0.1
commit_version 1.0.0
RECONCILE_BACKWARD_SHA=$VERSION_SHA
git -C "$WORK_REPOSITORY" push -q origin HEAD:refs/heads/release/1.0
case_name='reconcile-tags rejects a version that moves backward without creating tags'
if run_release_script reconcile-tags release/1.0 "$RECONCILE_BACKWARD_SHA" origin || [ -n "$(git ls-remote --tags "$BARE_ORIGIN")" ]; then fail "$case_name"; else pass "$case_name"; fi

new_repository reconcile-lightweight
commit_version 1.0.0
RECONCILE_LIGHTWEIGHT_SHA=$VERSION_SHA
git -C "$WORK_REPOSITORY" tag v1.0.0 "$RECONCILE_LIGHTWEIGHT_SHA"
git -C "$WORK_REPOSITORY" push -q origin HEAD:refs/heads/release/1.0 refs/tags/v1.0.0:refs/tags/v1.0.0
case_name='reconcile-tags rejects a lightweight tag'
if run_release_script reconcile-tags release/1.0 "$RECONCILE_LIGHTWEIGHT_SHA" origin || [ "$(remote_tag_target v1.0.0)" != '' ]; then fail "$case_name"; else pass "$case_name"; fi

new_repository reconcile-conflict
commit_version 1.0.0
RECONCILE_CONFLICT_ZERO_SHA=$VERSION_SHA
commit_version 1.0.1
RECONCILE_CONFLICT_ONE_SHA=$VERSION_SHA
git -C "$WORK_REPOSITORY" tag -a v1.0.0 "$RECONCILE_CONFLICT_ONE_SHA" -m 'Release v1.0.0'
git -C "$WORK_REPOSITORY" push -q origin HEAD:refs/heads/release/1.0 refs/tags/v1.0.0:refs/tags/v1.0.0
case_name='reconcile-tags rejects a tag at a different commit'
if run_release_script reconcile-tags release/1.0 "$RECONCILE_CONFLICT_ONE_SHA" origin || [ "$(remote_tag_target v1.0.0)" != "$RECONCILE_CONFLICT_ONE_SHA" ] || [ -n "$(remote_tag_object v1.0.1)" ]; then fail "$case_name"; else pass "$case_name"; fi

new_repository create
case_name='create-tag creates and pushes an annotated tag'
if ! run_release_script create-tag release/1.0 1.0.0 HEAD origin; then fail "$case_name"; fi
CREATED_TAG_OBJECT=$(remote_tag_object v1.0.0)
if [ -z "$CREATED_TAG_OBJECT" ] || [ "$(remote_tag_target v1.0.0)" != "$SECOND_SHA" ] || [ "$(git --git-dir="$BARE_ORIGIN" cat-file -t "$CREATED_TAG_OBJECT")" != tag ]; then fail "$case_name"; fi
pass "$case_name"
case_name='create-tag is a no-op for the same annotated remote tag'
if ! run_release_script create-tag release/1.0 1.0.0 HEAD origin || [ "$(remote_tag_object v1.0.0)" != "$CREATED_TAG_OBJECT" ]; then fail "$case_name"; else pass "$case_name"; fi

new_repository malformed-idempotent
git -C "$WORK_REPOSITORY" tag -a v1.0.0 "$SECOND_SHA" -m 'Release v1.0.0'
git -C "$WORK_REPOSITORY" tag -a v1.0.2 "$FIRST_SHA" -m 'Release v1.0.2'
git -C "$WORK_REPOSITORY" push -q origin refs/tags/v1.0.0:refs/tags/v1.0.0 refs/tags/v1.0.2:refs/tags/v1.0.2
case_name='create-tag rejects an idempotent tag when sibling history is gapped'
if run_release_script create-tag release/1.0 1.0.0 HEAD origin; then fail "$case_name"; else pass "$case_name"; fi

new_repository conflict
git -C "$WORK_REPOSITORY" tag -a v1.0.0 "$FIRST_SHA" -m 'Release v1.0.0'
git -C "$WORK_REPOSITORY" push -q origin refs/tags/v1.0.0:refs/tags/v1.0.0
case_name='create-tag rejects an annotated tag at another commit'
if run_release_script create-tag release/1.0 1.0.0 HEAD origin; then fail "$case_name"; else pass "$case_name"; fi

new_repository lightweight
git -C "$WORK_REPOSITORY" tag v1.0.0 "$SECOND_SHA"
git -C "$WORK_REPOSITORY" push -q origin refs/tags/v1.0.0:refs/tags/v1.0.0
case_name='create-tag rejects a lightweight tag'
if run_release_script create-tag release/1.0 1.0.0 HEAD origin; then fail "$case_name"; else pass "$case_name"; fi

for malformed_version in 1.0.08 1.0.1000000000; do
  new_repository "malformed-version-$malformed_version"
  case_name="create-tag rejects malformed project version $malformed_version without changing refs"
  if run_release_script create-tag release/1.0 "$malformed_version" HEAD origin >"$REPOSITORY_ROOT/output" 2>&1 || [ -n "$(git ls-remote --tags "$BARE_ORIGIN")" ]; then fail "$case_name"; else pass "$case_name"; fi
done

for malformed_tag in v1.0.08 v1.0.1000000000; do
  new_repository "malformed-tag-$malformed_tag"
  git -C "$WORK_REPOSITORY" tag -a "$malformed_tag" "$FIRST_SHA" -m "Release $malformed_tag"
  git -C "$WORK_REPOSITORY" push -q origin "refs/tags/$malformed_tag:refs/tags/$malformed_tag"
  case_name="create-tag rejects malformed remote tag $malformed_tag without changing refs"
  if run_release_script create-tag release/1.0 1.0.0 HEAD origin >"$REPOSITORY_ROOT/output" 2>&1 || [ -n "$(remote_tag_object v1.0.0)" ]; then fail "$case_name"; else pass "$case_name"; fi
done

new_repository patch-limit
git -C "$WORK_REPOSITORY" tag -a v1.0.999999999 "$FIRST_SHA" -m 'Release v1.0.999999999'
git -C "$WORK_REPOSITORY" push -q origin refs/tags/v1.0.999999999:refs/tags/v1.0.999999999
case_name='create-tag rejects incrementing the maximum supported patch without changing refs'
if run_release_script create-tag release/1.0 1.0.0 HEAD origin >"$REPOSITORY_ROOT/output" 2>&1 || [ -n "$(remote_tag_object v1.0.0)" ]; then fail "$case_name"; else pass "$case_name"; fi

malformed_file_index=0
for malformed_bytes in '1.0.0\n\n' '1.0.0\r\n' '1.0.0\0\n' '1.0.0\\u0000\n' '1.0.0\377\n'; do
  malformed_file_index=$((malformed_file_index + 1))
  new_repository "malformed-version-file-$malformed_file_index"
  commit_raw_version "$malformed_bytes"
  MALFORMED_SHA=$VERSION_SHA
  git -C "$WORK_REPOSITORY" push -q origin HEAD:refs/heads/release/1.0
  git -C "$WORK_REPOSITORY" fetch -q origin refs/heads/release/1.0:refs/remotes/origin/release/1.0
  case_name="read-version rejects malformed raw VERSION bytes $TEST_NUMBER"
  if run_release_script read-version 'HEAD:VERSION' >"$REPOSITORY_ROOT/current-output" 2>&1 || run_release_script read-version 'origin/release/1.0:VERSION' >"$REPOSITORY_ROOT/base-output" 2>&1 || run_release_script reconcile-tags release/1.0 "$MALFORMED_SHA" origin >"$REPOSITORY_ROOT/history-output" 2>&1 || [ -n "$(git ls-remote --tags "$BARE_ORIGIN")" ]; then fail "$case_name"; else pass "$case_name"; fi
done

new_repository missing-version-file
case_name='read-version rejects a missing head VERSION file'
if run_release_script read-version 'HEAD:VERSION'; then fail "$case_name"; else pass "$case_name"; fi
case_name='read-version-or-default uses the fallback only when VERSION is missing'
if [ "$(run_release_script read-version-or-default 'HEAD:VERSION' 0.0.0)" = "0.0.0$VERSION_SENTINEL" ]; then pass "$case_name"; else fail "$case_name"; fi
case_name='read-version-or-default rejects an unreadable ref instead of using the fallback'
if run_release_script read-version-or-default 'missing-ref:VERSION' 0.0.0; then fail "$case_name"; else pass "$case_name"; fi
case_name='read-version accepts only the exact canonical VERSION file and keeps its sentinel'
commit_version 1.0.0
if [ "$(run_release_script read-version 'HEAD:VERSION')" = "1.0.0$VERSION_SENTINEL" ]; then pass "$case_name"; else fail "$case_name"; fi
case_name='read-version-or-default returns the valid VERSION instead of the fallback'
if [ "$(run_release_script read-version-or-default 'HEAD:VERSION' 0.0.0)" = "1.0.0$VERSION_SENTINEL" ]; then pass "$case_name"; else fail "$case_name"; fi

new_repository invalid-default-source
commit_raw_version '1.0.0\r\n'
case_name='read-version-or-default rejects an invalid VERSION instead of using the fallback'
if run_release_script read-version-or-default 'HEAD:VERSION' 0.0.0; then fail "$case_name"; else pass "$case_name"; fi

new_repository migration-history
case_name='reconcile-tags ignores migration history before a matching release line'
commit_version 0.9.9
commit_version 1.0.0
VERSION_MIGRATION_SHA=$VERSION_SHA
git -C "$WORK_REPOSITORY" push -q origin HEAD:refs/heads/release/1.0
if ! run_release_script reconcile-tags release/1.0 "$VERSION_MIGRATION_SHA" origin || [ "$(remote_tag_target v1.0.0)" != "$VERSION_MIGRATION_SHA" ]; then fail "$case_name"; else pass "$case_name"; fi

new_repository missing-before-release
commit_version 1.0.0
MISSING_BEFORE_RELEASE_SHA=$VERSION_SHA
git -C "$WORK_REPOSITORY" push -q origin HEAD:refs/heads/release/1.0
case_name='reconcile-tags ignores missing VERSION before release history starts'
if ! run_release_script reconcile-tags release/1.0 "$MISSING_BEFORE_RELEASE_SHA" origin || [ "$(remote_tag_target v1.0.0)" != "$MISSING_BEFORE_RELEASE_SHA" ]; then fail "$case_name"; else pass "$case_name"; fi

printf '1..%s\n' "$TEST_NUMBER"
