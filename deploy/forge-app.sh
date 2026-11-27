# shellcheck shell=bash
# The GitHub App Sortie mints each attempt's GitHub tokens from (WORKFLOW.md
# environment.remote.forge_app; spec store job-scoped-github-tokens). Its
# private key is in the stage-0 home on p1 only, owned by the operator and
# readable by nobody else: Sortie alone reads it, at start. Sourced by
# deploy.sh.
# shellcheck disable=SC2034 # deploy.sh reads it
ORTUS_NOVA_GITHUB_APP_KEY=$ORTUS_NOVA_HOME/github-app.pem

# forge_app_key_ok FILE: succeed when FILE is a regular file of this user
# with mode 0600 or narrower: no execute bit, and nothing for a group or
# others; else say why on stderr and fail. It never reads the key.
forge_app_key_ok() {
  local file=$1 mode owner
  if [[ ! -f $file ]]; then
    printf "the GitHub App's private key is not at %s: put it there, mode 0600 (README.md \"Credentials\")\n" "$file" >&2
    return 1
  fi
  mode=$(stat -c %a "$file") owner=$(stat -c %u "$file")
  if (( 8#$mode & ~8#600 )); then
    printf "the GitHub App's private key %s has mode %s, wider than 600: chmod 600 %s\n" "$file" "$mode" "$file" >&2
    return 1
  fi
  if [[ $owner != "$(id -u)" ]]; then
    printf "the GitHub App's private key %s belongs to uid %s, not to %s\n" "$file" "$owner" "$(id -un)" >&2
    return 1
  fi
}
