#!/bin/sh
# pin-recipe.sh — regenerate internal/proxyrecipe/recipe.lock from a proxy recipe
# directory (Dockerfile + entrypoint.sh). Run after the dotfiles proxy recipe
# legitimately changes; commit the updated recipe.lock and re-release ws. This is
# the runtime analogue of bumping DOTFILES_REF in CI.
#
# Usage: scripts/pin-recipe.sh <recipe-dir> [dotfiles-ref]
set -eu

recipe_dir="${1:?usage: pin-recipe.sh <recipe-dir> [dotfiles-ref]}"
dotfiles_ref="${2:-unknown}"
out="internal/proxyrecipe/recipe.lock"

sha() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}';
	else shasum -a 256 "$1" | awk '{print $1}'; fi
}

[ -f "${recipe_dir}/Dockerfile" ]    || { echo "no Dockerfile in ${recipe_dir}" >&2; exit 1; }
[ -f "${recipe_dir}/entrypoint.sh" ] || { echo "no entrypoint.sh in ${recipe_dir}" >&2; exit 1; }

d_hash=$(sha "${recipe_dir}/Dockerfile")
e_hash=$(sha "${recipe_dir}/entrypoint.sh")

cat > "$out" <<EOF
{
  "datapath_mode": "tproxy",
  "dotfiles_ref": "${dotfiles_ref}",
  "files": {
    "Dockerfile": "${d_hash}",
    "entrypoint.sh": "${e_hash}"
  }
}
EOF

echo "wrote ${out} (Dockerfile=${d_hash} entrypoint.sh=${e_hash} ref=${dotfiles_ref})"
