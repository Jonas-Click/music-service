_music_complete() {
    local cur prev
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    local cmds="play stop vol raw status songs settings engine help"
    if [ "$COMP_CWORD" -eq 1 ]; then
        COMPREPLY=($(compgen -W "$cmds" -- "$cur"))
        return
    fi
    local first="${COMP_WORDS[1]}"
    if [ "$first" = "play" ] && [ "$COMP_CWORD" -eq 2 ]; then
        COMPREPLY=($(compgen -W "1 2 3 4 5 6 7" -- "$cur"))
    elif [ "$first" = "engine" ] && [ "$COMP_CWORD" -eq 2 ]; then
        COMPREPLY=($(compgen -W "on off ride for vol style demo help" -- "$cur"))
    elif [ "$first" = "engine" ] && [ "$COMP_CWORD" -eq 3 ] && [ "${COMP_WORDS[2]}" = "style" ]; then
        COMPREPLY=($(compgen -W "v8 moped sport vtwin electric" -- "$cur"))
    elif [ "$first" = "engine" ] && [ "$COMP_CWORD" -eq 3 ] && [ "${COMP_WORDS[2]}" = "for" ]; then
        COMPREPLY=($(compgen -W "1m 5m 10m 30m 1h" -- "$cur"))
    elif [ "$first" = "settings" ] && [ "$COMP_CWORD" -eq 2 ]; then
        COMPREPLY=($(compgen -W "music.volume unlock.enabled unlock.song unlock.volume unlock.duration unlock.trigger_field unlock.trigger_value unlock.trigger_from engine.enabled engine.volume engine.style" -- "$cur"))
    elif [ "$first" = "settings" ] && [ "$COMP_CWORD" -eq 3 ]; then
        case "${COMP_WORDS[2]}" in
            unlock.enabled|engine.enabled) COMPREPLY=($(compgen -W "true false" -- "$cur")) ;;
            engine.style) COMPREPLY=($(compgen -W "v8 moped sport vtwin electric" -- "$cur")) ;;
        esac
    fi
}
complete -F _music_complete music
