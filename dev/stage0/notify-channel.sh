#!/usr/bin/env bash
# The operator's channel (spec store agent-platform/run-supervision: a
# person is alerted, wherever the question is held). Both workflows send each
# question for a person to it through Sortie's notifications; the workbench
# pushes the same questions there with a link to where each is answered, and
# again every 4 hours while one stays open. The operator chooses it in
# stage0.env:
#
#   STAGE0_NOTIFY_KIND     slack, discord, telegram or webhook (Sortie's
#                          notification kinds); empty: no channel
#   STAGE0_NOTIFY_CHAT_ID  the chat, for telegram only
#
# and writes its credential, the webhook URL or the Telegram bot's token, into
# $STAGE0_HOME/notify-credential, mode 0600. Nothing in the kit writes that
# file. Sourced by stage0.sh, run-sortie.sh and run-workbench.sh.

# What Sortie sends there: a run that stopped for a person (an agent's park,
# a review that cannot settle a criterion), a question its broker escalates,
# and the holds and merge failures that need a person.
NOTIFY_EVENTS='[session.stopped, session.escalated, budget.held, escalation.auto_merge]'

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
    *) echo "usage: $0 entry KIND [CHAT_ID] | check FILE" >&2; exit 2 ;;
  esac
fi
