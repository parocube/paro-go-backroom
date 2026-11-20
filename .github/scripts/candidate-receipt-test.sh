#!/usr/bin/env bash
set -euo pipefail
script="$(cd "$(dirname "$0")" && pwd)/candidate-receipt.sh"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export RUNNER_TEMP="$tmp" GITHUB_ENV="$tmp/env" GITHUB_REPOSITORY=example/app
export SOURCE=1111111111111111111111111111111111111111 PR_NUMBER=3 RELEASE=release/1.0
mkdir "$tmp/bin"
cat > "$tmp/bin/gh" <<'MOCK'
#!/usr/bin/env bash
case "$1 $2" in
  'api --paginate') echo 23 ;;
  'api repos/example/app/actions/runs/23') cat "$RUNNER_TEMP/record" ;;
  'run download') cp "$RUNNER_TEMP/artifact" "$RUNNER_TEMP/receipt.json" ;;
  *) exit 1 ;;
esac
MOCK
chmod +x "$tmp/bin/gh"
export PATH="$tmp/bin:$PATH"
jq -n --arg sha "$SOURCE" '{path:".github/workflows/release-tag.yml",event:"pull_request",head_repository:{full_name:"example/app"},head_branch:"release/1.0",head_sha:$sha,conclusion:"success"}' > "$tmp/good"
jq -n --arg sha "$SOURCE" '{source:$sha,pr:"3"}' > "$tmp/artifact"
cp "$tmp/good" "$tmp/record"
bash "$script"
grep -qx CANDIDATE_RUN=23 "$GITHUB_ENV"
for change in '.conclusion="failure"' '.head_sha="other"' '.head_branch="feature"' '.head_repository.full_name="fork/app"' '.path="other.yml"' '.event="push"'; do
  jq "$change" "$tmp/good" > "$tmp/record"
  if bash "$script"; then echo "accepted invalid run: $change" >&2; exit 1; fi
done
cp "$tmp/good" "$tmp/record"
printf '{"source":"%s","pr":"4"}\n' "$SOURCE" > "$tmp/artifact"
if bash "$script"; then echo 'accepted a receipt from another PR' >&2; exit 1; fi
