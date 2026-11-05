#!/usr/bin/env bash
# Where Sortie's notifications go (spec store agent-platform/run-supervision:
# a person is alerted, wherever the question is held).
#
# The default destination is the workbench, by Sortie's webhook backend: the
# workflow's entry posts to the workbench's notifications endpoint, at a URL
# that names this host and carries its token, which stage0.sh mints into
# $STAGE0_HOME/notify-tokens/<host>.token (0600) and run-sortie.sh reads into
# SORTIE_WORKBENCH_NOTIFY_URL. It needs no operator credential.
#
# The operator may add a channel of their own, one more entry. The workflow
# sends each question for a person to it through Sortie's notifications; the
# workbench pushes the same questions there with a link to where each is
# answered, and again every 4 hours while one stays open. The operator
# chooses it in stage0.env:
#
#   STAGE0_NOTIFY_KIND     slack, discord, telegram or webhook (Sortie's
#                          notification kinds); empty: no channel
#   STAGE0_NOTIFY_CHAT_ID  the chat, for telegram only
#
# and writes its credential, the webhook URL or the Telegram bot's token, into
# $STAGE0_HOME/notify-credential, mode 0600. Nothing in the kit writes that
# file. Sourced by stage0.sh and run-sortie.sh; run-workbench.sh hands the
# file's path to the workbench. Optional: with no kind, nothing needs it, and
# the workbench is the only destination.

# What Sortie sends there: a run that stopped for a person (an agent's park,
# a review that cannot settle a criterion), a question its broker escalates,
# and the holds, merge failures, and pull requests still failing or
# conflicting after their fix runs, that need a person; and a story Sortie
# reverted after merge-and-prove found it unproven (story.reverted).
NOTIFY_EVENTS='[session.stopped, session.escalated, budget.held, escalation.auto_merge, escalation.ci_failure, escalation.merge_conflicts, story.reverted]'

# What Sortie sends the workbench (WORKFLOW.md lists the same): a run that
# stopped for a person or failed, a question its broker escalates, every
# escalation, a budget hold, and a story Sortie reverted.
# shellcheck disable=SC2034 # the kit's tests check WORKFLOW.md against it
WORKBENCH_EVENTS='[session.stopped, session.failed, session.escalated, escalation.ci_failure, escalation.review_comments, escalation.bot_review, escalation.merge_conflicts, escalation.auto_merge, escalation.merge_completion, budget.held, story.reverted]'

# The port p1's workbench has on a worker host's loopback, through the
# tunnel p1 opens to it (worker-host.sh); the same on every host.
STAGE0_WORKBENCH_TUNNEL_PORT=${STAGE0_WORKBENCH_TUNNEL_PORT:-7790}

# notify_token HOME HOST: mint HOST's token for the workbench's
# notifications into HOME/notify-tokens/HOST.token, mode 0600, unless it is
# there; no person writes it.
notify_token() {
  local dir=$1/notify-tokens
  [[ $2 =~ ^[a-z0-9][a-z0-9-]*$ ]] || { echo "the host name '$2' is not lower-case letters, digits and dashes" >&2; return 1; }
  mkdir -p "$dir" && chmod 700 "$dir" || return 1
  [[ -s $dir/$2.token ]] && return 0
  (umask 077; od -An -N24 -tx1 /dev/urandom | tr -d ' \n' > "$dir/$2.token")
  [[ -s $dir/$2.token ]]
}

# notify_host: this host's name, as the workbench names its Sortie
# (run-workbench.sh) and its notifications token.
notify_host() {
  local host=${STAGE0_SORTIE_NAME:-$(hostname -s | tr '[:upper:]' '[:lower:]')}
  [[ $host =~ ^[a-z0-9][a-z0-9-]*$ ]] || { echo "the host name '$host' is not lower-case letters, digits and dashes: set STAGE0_SORTIE_NAME in stage0.env" >&2; return 1; }
  printf '%s\n' "$host"
}

# workbench_notify_url HOME PORT: the URL this host's Sortie posts its
# notifications to, with the host's token in it: on p1 PORT is the
# workbench's, on a worker host the tunnel's (STAGE0_WORKBENCH_TUNNEL_PORT).
# Fails, saying why, when the token is missing or others can read it.
workbench_notify_url() {
  local home=$1 port=$2 host token
  host=$(notify_host) || return 1
  token=$home/notify-tokens/$host.token
  [[ -s $token ]] || { echo "$token is missing: run dev/stage0/stage0.sh, which mints it (on a worker host, worker-host.sh add on p1 copies it)" >&2; return 1; }
  [[ $(stat -c %a "$token") == 600 ]] || { echo "$token must be mode 0600: chmod 600 $token" >&2; return 1; }
  printf 'http://127.0.0.1:%s/api/notifications/%s/%s\n' "$port" "$host" "$(cat "$token")"
}

# notify_entry KIND [CHAT_ID]: the workflow's notifications entry for the
# channel, indented as a list item; nothing for no kind. The credential stays
# a reference to Sortie's environment (run-sortie.sh).
notify_entry() {
  local kind=$1 chat=${2:-}
  # shellcheck disable=SC2016 # $SORTIE_NOTIFY_SECRET is Sortie's to resolve
  case $kind in
    '') return 0 ;;
    slack|discord) printf '  - kind: %s\n    webhook_url: $SORTIE_NOTIFY_SECRET\n    events: %s\n' "$kind" "$NOTIFY_EVENTS" ;;
    webhook) printf '  - kind: webhook\n    url: $SORTIE_NOTIFY_SECRET\n    events: %s\n' "$NOTIFY_EVENTS" ;;
    telegram)
      [[ $chat =~ ^(-?[0-9]+|@[A-Za-z0-9_]+)$ ]] || { echo "STAGE0_NOTIFY_CHAT_ID must be a chat id or @channel for telegram, not '$chat'" >&2; return 1; }
      printf '  - kind: telegram\n    bot_token: $SORTIE_NOTIFY_SECRET\n    chat_id: "%s"\n    events: %s\n' "$chat" "$NOTIFY_EVENTS" ;;
    *) echo "STAGE0_NOTIFY_KIND is '$kind'; want slack, discord, telegram or webhook" >&2; return 1 ;;
  esac
}

# notify_credential FILE: fail, saying why, unless FILE holds a credential
# that only its owner, the operator, can read or write.
notify_credential() {
  local file=$1
  [[ -s $file ]] || { echo "$file is missing: write the channel's webhook URL or bot token into it, mode 0600" >&2; return 1; }
  [[ -O $file ]] || { echo "$file must belong to $(id -un)" >&2; return 1; }
  [[ $(stat -c %a "$file") == 600 ]] || { echo "$file must be mode 0600: chmod 600 $file" >&2; return 1; }
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
  case ${1:-} in
    entry) shift; notify_entry "$@" ;;
    check) shift; notify_credential "$@" ;;
    workbench-url) shift; workbench_notify_url "$@" ;;
    *) echo "usage: $0 entry KIND [CHAT_ID] | check FILE | workbench-url HOME PORT" >&2; exit 2 ;;
  esac
fi
