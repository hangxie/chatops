#!/bin/bash

# Create the chatops Slack app from scripts/slack-app-manifest.json using
# Slack's App Manifest API. This configures Socket Mode, Interactivity, the
# event subscriptions, and the bot scopes that the server requires.
#
# It cannot mint the two credentials the server consumes: the bot token comes
# from installing the app to a workspace and the app-level token is generated
# in the browser. Both are printed as follow-up links when the app is created.
#
# Usage: create-slack-app.sh [app-name]
#
# The optional app-name overrides the "chatops" name in the manifest (both the
# app display name and the bot user name); it can also be set with SLACK_APP_NAME.
#
# Requirements: curl, jq, and a configuration access token exported as
# SLACK_CONFIG_ACCESS_TOKEN. Generate the token (valid 12 hours) at
# https://api.slack.com/reference/manifests#config-tokens

set -euo pipefail

MANIFEST="$(cd "$(dirname "$0")" && pwd)/slack-app-manifest.json"
APP_NAME="${1:-${SLACK_APP_NAME:-}}"

if [[ -z "${SLACK_CONFIG_ACCESS_TOKEN:-}" ]]; then
    echo "error: SLACK_CONFIG_ACCESS_TOKEN is not set" >&2
    echo "generate one at https://api.slack.com/reference/manifests#config-tokens" >&2
    exit 1
fi

for cmd in curl jq; do
    if ! command -v "${cmd}" >/dev/null 2>&1; then
        echo "error: ${cmd} is required" >&2
        exit 1
    fi
done

if [[ ! -f "${MANIFEST}" ]]; then
    echo "error: manifest not found at ${MANIFEST}" >&2
    exit 1
fi

if [[ -n "${APP_NAME}" ]]; then
    manifest="$(jq -c --arg name "${APP_NAME}" \
        '.display_information.name = $name | .features.bot_user.display_name = $name' \
        "${MANIFEST}")"
else
    manifest="$(jq -c . "${MANIFEST}")"
fi

response="$(curl -sS -X POST https://slack.com/api/apps.manifest.create \
    -H "Authorization: Bearer ${SLACK_CONFIG_ACCESS_TOKEN}" \
    --data-urlencode "manifest=${manifest}")"

if [[ "$(jq -r '.ok' <<<"${response}")" != "true" ]]; then
    echo "error: Slack API rejected the request:" >&2
    jq -r '.error // "unknown error"' <<<"${response}" >&2
    jq -r '.errors[]? | "  - \(.message)"' <<<"${response}" >&2
    exit 1
fi

app_id="$(jq -r '.app_id' <<<"${response}")"

echo "Created Slack app: ${app_id}"
echo "Manage it at https://api.slack.com/apps/${app_id}"
echo
echo "Two credentials still require the browser:"
echo "  1. Install the app to a workspace and store the xoxb token as slack-bot-token:"
echo "       https://api.slack.com/apps/${app_id}/install-on-team"
echo "  2. Generate an app-level token with the connections:write scope for"
echo "     store it as slack-app-token under \"App-Level Tokens\":"
echo "       https://api.slack.com/apps/${app_id}/general"
