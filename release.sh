#!/usr/bin/env bash
set -euo pipefail

MODULE="github.com/struct0x/initd"
DRY_RUN=false

# Auto-detect sub-modules: directories with a go.mod tracked by git, excluding example
SUBMODS=()
for d in */go.mod; do
  dir="$(dirname "$d")"
  [[ "$dir" == "example" ]] && continue
  if git ls-files --error-unmatch "$d" &>/dev/null; then
    SUBMODS+=("$dir")
  fi
done

usage() {
  local latest
  latest="$(git tag --list 'v*' --sort=-version:refname | head -1)"
  echo "Usage: $0 [--dry-run] <version>"
  echo "  e.g. $0 v0.1.0"
  echo "       $0 --dry-run v0.1.0"
  echo ""
  echo "Latest released version: ${latest:-"(none)"}"
  echo ""
  echo "Procedure:"
  echo "  1. Updates the root module version in each committed sub-module's go.mod"
  echo "  2. Runs 'go mod tidy' in each sub-module and the root"
  echo "  3. Commits the changes as 'release: <version>'"
  echo "  4. Tags each committed sub-module as '<submod>/<version>'"
  echo "  5. Tags the root module as '<version>'"
  echo ""
  echo "Only sub-modules with a committed go.mod are included."
  echo "After running, push with: git push origin main --tags"
  exit 1
}

run() {
  if $DRY_RUN; then
    echo "  [dry-run] $*"
  else
    "$@"
  fi
}

# Parse flags
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY_RUN=true; shift ;;
    -*)        usage ;;
    *)         VERSION="$1"; shift ;;
  esac
done

[[ -n "${VERSION:-}" ]] || usage

if [[ ${#SUBMODS[@]} -eq 0 ]]; then
  echo "  note: no committed sub-modules found, tagging root only"
fi

# Validate version format
if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-.*)?$ ]]; then
  echo "error: version must match vX.Y.Z (got: $VERSION)"
  exit 1
fi

# Ensure clean working tree (untracked files are ignored)
if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  echo "error: working tree is dirty, commit or stash changes first"
  exit 1
fi

echo "Releasing $VERSION"

# Update require versions in all sub-module go.mod files
for mod in "${SUBMODS[@]}"; do
  gomod="$mod/go.mod"
  echo "  updating $gomod"

  # Replace any version of the root module
  run sed -i '' -E "s|($MODULE) v[^ ]+|\1 $VERSION|" "$gomod"

  # Replace any version of sibling sub-modules (for example/)
  for sibling in "${SUBMODS[@]}"; do
    [[ "$sibling" == "example" ]] && continue
    run sed -i '' -E "s|($MODULE/$sibling) v[^ ]+|\1 $VERSION|" "$gomod"
  done
done

# Run go mod tidy in each sub-module
for mod in "${SUBMODS[@]}"; do
  echo "  tidying $mod"
  run bash -c "cd $mod && go mod tidy"
done

# Tidy root
echo "  tidying root"
run go mod tidy

# Commit — only stage files the script touched (go.mod/go.sum of tracked modules)
if ! $DRY_RUN; then
  git add go.mod go.sum
  for mod in "${SUBMODS[@]}"; do
    git add "$mod/go.mod" "$mod/go.sum" 2>/dev/null || true
  done
  if git diff --cached --quiet; then
    echo "  no changes to commit (versions already at $VERSION)"
  else
    git commit -m "release: $VERSION"
  fi
else
  echo "  [dry-run] git commit -m \"release: $VERSION\""
fi

# Tag sub-modules first, root last
for mod in "${SUBMODS[@]}"; do
  echo "  tagging $mod/$VERSION"
  run git tag -a "$mod/$VERSION" -m "$mod/$VERSION"
done
echo "  tagging $VERSION"
run git tag -a "$VERSION" -m "$VERSION"

echo ""
echo "Done. Tags created locally. Push with:"
echo "  git push origin main --tags"
