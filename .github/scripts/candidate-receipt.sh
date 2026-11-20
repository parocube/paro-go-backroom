#!/usr/bin/env bash
set -euo pipefail

name="candidate-$PR_NUMBER-$SOURCE"
api="repos/$GITHUB_REPOSITORY/actions"
# A SHA can have multiple attempts. Only trust artifacts of a completed, successful release PR run.
runs=$(gh api --paginate "$api/artifacts?name=$name&per_page=100" --jq '.artifacts[] | select(.expired == false) | .workflow_run.id')
for run in $runs; do
  record=$(gh api "$api/runs/$run")
  if ! jq -e --arg source "$SOURCE" --arg branch "$RELEASE" --arg repo "$GITHUB_REPOSITORY" '
    .path == ".github/workflows/release-tag.yml" and .event == "pull_request" and
    .head_repository.full_name == $repo and .head_branch == $branch and
    .head_sha == $source and .conclusion == "success"
  ' <<<"$record" >/dev/null; then continue; fi
  gh run download "$run" --repo "$GITHUB_REPOSITORY" --name "$name" --dir "$RUNNER_TEMP"
  jq -e --arg source "$SOURCE" --arg pr "$PR_NUMBER" '.source == $source and .pr == $pr' "$RUNNER_TEMP/receipt.json" >/dev/null
  echo "CANDIDATE_RUN=$run" >> "$GITHUB_ENV"
  exit 0
done
echo "Missing successful candidate for PR $PR_NUMBER at $SOURCE" >&2
exit 1
