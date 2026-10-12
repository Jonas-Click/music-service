_music_complete() {
    local cur prev
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    local cmds="play stop vol raw status songs settings help"
    if [ "$COMP_CWORD" -eq 1 ]; then
        COMPREPLY=($(compgen -W "$cmds" -- "$cur"))
        return
    fi
    local first="${COMP_WORDS[1]}"
    if [ "$first" = "play" ] && [ "$COMP_CWORD" -eq 2 ]; then
        COMPREPLY=($(compgen -W "1 2 3 4 5 6 7" -- "$cur"))
    elif [ "$first" = "settings" ] && [ "$COMP_CWORD" -eq 2 ]; then
        COMPREPLY=($(compgen -W "unlock.enabled unlock.song unlock.volume unlock.duration unlock.trigger_field unlock.trigger_value unlock.trigger_from" -- "$cur"))
    elif [ "$first" = "settings" ] && [ "$COMP_CWORD" -eq 3 ]; then
        case "${COMP_WORDS[2]}" in
            unlock.enabled) COMPREPLY=($(compgen -W "true false" -- "$cur")) ;;
        esac
    fi
}
complete -F _music_complete music
