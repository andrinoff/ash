#!/bin/sh
# took: how long the last command took, as the prompt segment "took"
# ($ash_plugin_took), when it ran for two seconds or more.
#
#     plugin load took ~/src/ash/examples/plugins/took.sh
#
# The built-in prompt shows this already; the plugin is for your own
# ash_prompt function or for starship. It's also about the smallest
# plugin there is. Needs jq.

# Say hello first: we only want to hear about finished commands.
echo '{"hello":{"name":"took","events":["postexec"]}}'

# Then read one event per line until ash closes our input.
while IFS= read -r event; do
    ms=$(printf '%s\n' "$event" | jq -r 'select(.event == "postexec") | .duration_ms')
    [ -n "$ms" ] || continue
    text=
    if [ "$ms" -ge 2000 ]; then
        text=$(awk -v ms="$ms" 'BEGIN {
            s = ms / 1000
            if (s < 60) printf "took %.1fs", s
            else printf "took %dm%02ds", s / 60, s % 60
        }')
    fi
    # An empty text clears the segment.
    jq -cn --arg text "$text" '{segment: {name: "took", text: $text}}'
done
