#!/usr/bin/env bash
# Send each case's prompt to a running chatops over its unix socket, record the reply, and grade it.
# Usage: scripts/model-check.sh [-s socket] [-o result-file] [-t seconds] [-n note] cases-file
# Tool checks read the agent.show_stats footer; without it they are left for a human (MANUAL).
set -euo pipefail

socket=/run/chatops/ops.sock
out=""
limit=300
note=""
read -ra nc_cmd <<<"${NC:-nc -NU}"
# The footer's totals line, as internal/conversation/footer.go writes it.
footer_re='^total [^ ]+ · [0-9]+ tools? · (.* · )?reply [^ ]+( · (stopped|timed out|failed))?$'

usage() {
    sed -n '3,4s/^# //p' "$0" >&2
    exit 2
}

while getopts "s:o:t:n:h" opt; do
    case $opt in
    s) socket=$OPTARG ;;
    o) out=$OPTARG ;;
    t) limit=$OPTARG ;;
    n) note=$OPTARG ;;
    *) usage ;;
    esac
done
shift $((OPTIND - 1))
[ $# -eq 1 ] || usage
cases=$1
[ -r "$cases" ] || { echo "model-check: cannot read $cases" >&2; exit 2; }
timeout_cmd=$(command -v timeout || command -v gtimeout || true)
[ -n "$timeout_cmd" ] || { echo "model-check: needs timeout (GNU coreutils) to enforce -t" >&2; exit 2; }
[ -n "$out" ] || out="model-check-$(date +%Y%m%d-%H%M%S).md"

lower() { tr '[:upper:]' '[:lower:]'; }
# plain folds the Unicode hyphens and spaces models like to emit, so has= and lacks= match "worker-0".
plain() { LC_ALL=C sed -e $'s/\xe2\x80[\x90-\x95]/-/g' -e $'s/\xc2\xa0/ /g' -e $'s/\xe2\x80\xaf/ /g'; }
trim() { sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//'; }

# ask sends one prompt and prints the raw reply; a timeout or connection failure prints nothing.
ask() { printf '%s\n' "$1" | "$timeout_cmd" "$limit" "${nc_cmd[@]}" "$socket" 2>/dev/null || true; }

# tool_steps prints the tool names of a footer's step line, one per line, in call order.
tool_steps() {
    printf '%s\n' "$1" | awk -F' → ' '{
        for (i = 1; i <= NF; i++) {
            split($i, w, " ")
            if (w[1] != "plan" && w[1] != "answer" && w[1] != "model") print w[1]
        }
    }'
}

# has_tool reports whether a tool list names $2 exactly, as server.tool or bare tool.
has_tool() { printf '%s\n' "$1" | awk -v t="$2" '{ n = $0; sub(/^[^.]*\./, "", n) } $0 == t || n == t { f = 1 } END { exit !f }'; }

total=0 passed=0 failed=0 manual=0
summary=""
{
    echo "# chatops model check"
    echo
    echo "- Date: $(date '+%Y-%m-%d %H:%M:%S %z')"
    echo "- Socket: \`$socket\`"
    echo "- Cases: \`$cases\`"
    [ -z "$note" ] || echo "- Note: $note"
    echo
} >"$out"

while IFS= read -r line || [ -n "$line" ]; do
    case $line in '' | '#'*) continue ;; esac
    id=$(printf '%s' "$line" | cut -d'|' -f1 | trim)
    prompt=$(printf '%s' "$line" | cut -d'|' -f2 | trim)
    checks=$(printf '%s' "$line" | cut -d'|' -f3- | trim)
    [ -n "$prompt" ] || continue
    total=$((total + 1))
    num=$(printf '%02d' "$total")
    printf '%s. %s ... ' "$num" "$id" >&2

    start=$(date +%s)
    reply=$(ask "$prompt")
    elapsed=$(($(date +%s) - start))

    # The footer ends the reply: a step line that starts with a model step, then the totals line.
    total_line=$(printf '%s\n' "$reply" | tail -n1 | grep -E "$footer_re" || true)
    step_line="" body=$reply
    if [ -n "$total_line" ]; then
        step_line=$(printf '%s\n' "$reply" | tail -n2 | head -n1 | grep -E '^(plan|answer|model) ' || true)
        footer_lines=1
        [ -z "$step_line" ] || footer_lines=2
        body=$(printf '%s\n' "$reply" | awk -v d="$footer_lines" '{ l[NR] = $0 } END { for (i = 1; i <= NR - d; i++) print l[i] }')
    fi
    tools=$(tool_steps "$step_line")
    body_lc=$(printf '%s' "$body" | plain | lower)

    problems="" unchecked=""
    if [ -z "$reply" ]; then
        problems="no reply within ${limit}s or socket unreachable"
    elif printf '%s' "$total_line" | grep -Eq '· (stopped|timed out|failed)$'; then
        problems="turn ${total_line##*· }"
    elif printf '%s' "$body" | grep -q '^error:'; then
        problems="turn failed"
    fi
    IFS=';' read -ra items <<<"$checks"
    for item in "${items[@]+"${items[@]}"}"; do
        item=$(printf '%s' "$item" | trim)
        [ -n "$item" ] || continue
        key=${item%%=*} val=${item#*=}
        case $key in
        tool | notool | first)
            if [ -z "$total_line" ]; then
                unchecked="$unchecked; $item (no stats footer)"
                continue
            fi
            ;;
        esac
        case $key in
        tool) has_tool "$tools" "$val" || problems="$problems; $val not called" ;;
        notool) has_tool "$tools" "$val" && problems="$problems; $val called" ;;
        first) has_tool "$(printf '%s\n' "$tools" | head -n1)" "$val" || problems="$problems; first tool was not $val" ;;
        has) case $body_lc in *"$(printf '%s' "$val" | lower)"*) ;; *) problems="$problems; reply lacks \"$val\"" ;; esac ;;
        lacks) case $body_lc in *"$(printf '%s' "$val" | lower)"*) problems="$problems; reply contains \"$val\"" ;; esac ;;
        *) unchecked="$unchecked; $item (unknown check)" ;;
        esac
    done
    problems=${problems#; } unchecked=${unchecked#; }

    if [ -n "$problems" ]; then
        verdict=FAIL failed=$((failed + 1))
    elif [ -z "$checks" ] || [ -n "$unchecked" ]; then
        verdict=MANUAL manual=$((manual + 1))
    else
        verdict=PASS passed=$((passed + 1))
    fi
    echo "$verdict (${elapsed}s)" >&2
    # The tool column shows warm-up calls and tool-less answers at a glance.
    tool_list=$(printf '%s\n' "$tools" | paste -sd, - | sed 's/,/, /g')
    summary="$summary| $num | $id | $verdict | ${elapsed}s | ${tool_list:--} | ${problems:-${unchecked:-}} |"$'\n'

    {
        echo "## $num. $id: $verdict"
        echo
        echo "- Prompt: \`$prompt\`"
        echo "- Checks: \`${checks:-none}\`"
        echo "- Time: ${elapsed}s"
        [ -z "$problems" ] || echo "- Failed: $problems"
        [ -z "$unchecked" ] || echo "- For a human: $unchecked"
        echo
        # Four backticks, so a code block in the reply does not end the fence.
        echo '````text'
        printf '%s\n' "${reply:-(no reply)}"
        echo '````'
        echo
    } >>"$out"
done <"$cases"

{
    echo "## Summary"
    echo
    echo "$passed passed, $failed failed, $manual manual, $total total"
    echo
    echo "| # | Case | Verdict | Time | Tools | Notes |"
    echo "|---|---|---|---|---|---|"
    printf '%s' "$summary"
} >>"$out"

echo "$passed passed, $failed failed, $manual manual, $total total; results in $out" >&2
[ "$failed" -eq 0 ]
