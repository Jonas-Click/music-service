_music_complete() {
    local cur
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    # The binary itself, not the alias: aliases aren't expanded in here.
    local bin=/data/music-service/music-service
    local cmds="play stop vol raw status songs settings engine help"
    if [ "$COMP_CWORD" -eq 1 ]; then
        COMPREPLY=($(compgen -W "$cmds" -- "$cur"))
        return
    fi
    local first="${COMP_WORDS[1]}"
    if [ "$first" = "play" ] && [ "$COMP_CWORD" -eq 2 ]; then
        COMPREPLY=($(compgen -W "$("$bin" ctl songs 2>/dev/null | awk '{print $1}')" -- "$cur"))
    elif [ "$first" = "engine" ] && [ "$COMP_CWORD" -eq 2 ]; then
        COMPREPLY=($(compgen -W "on off ride for vol style demo help" -- "$cur"))
    elif [ "$first" = "engine" ] && [ "$COMP_CWORD" -eq 3 ] && [ "${COMP_WORDS[2]}" = "style" ]; then
        COMPREPLY=($(compgen -W "$("$bin" ctl settings --values engine.style 2>/dev/null)" -- "$cur"))
    elif [ "$first" = "engine" ] && [ "$COMP_CWORD" -eq 3 ] && [ "${COMP_WORDS[2]}" = "for" ]; then
        COMPREPLY=($(compgen -W "1m 5m 10m 30m 1h" -- "$cur"))
    elif [ "$first" = "settings" ] && [ "$COMP_CWORD" -eq 2 ]; then
        COMPREPLY=($(compgen -W "$("$bin" ctl settings --keys 2>/dev/null)" -- "$cur"))
    elif [ "$first" = "settings" ] && [ "$COMP_CWORD" -eq 3 ]; then
        COMPREPLY=($(compgen -W "$("$bin" ctl settings --values "${COMP_WORDS[2]}" 2>/dev/null)" -- "$cur"))
    fi
}
complete -F _music_complete music
