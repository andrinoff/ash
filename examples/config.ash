# Example ~/.config/ash/config.ash
#
# ash reads both fish and sh syntax; this file uses a bit of each to show
# that they mix.

# PATH. add_path skips directories that don't exist and ones already there.
add_path ~/bin ~/.local/bin ~/go/bin ~/.cargo/bin
export EDITOR=nvim
export PAGER=less LESS=-R

if status is-interactive
    # Prompt, directory jumping, fuzzy finding, per-project tool versions.
    # Each one is skipped quietly if the tool isn't installed.
    integrate starship zoxide fzf mise direnv

    # Collapse the prompt to "❯ cmd" in scrollback.
    function starship_transient_prompt_func
        starship module character
    end
    enable_transience

    # Aliases run as written; abbreviations expand as you type, so history
    # shows the real command.
    alias ll 'ls -lh'
    alias la 'ls -lAh'
    abbr -a gs git status
    abbr -a gd git diff
    abbr -a gco git checkout
    abbr -a --set-cursor gcm 'git commit -m "%"'

    # sh-style functions work too.
    mkcd() { mkdir -p -- "$1" && cd -- "$1"; }

    # Bindings run shell code with the terminal in normal mode, so full
    # screen programs are fine here.
    bind ctrl-g 'lazygit; commandline -f repaint'

    # Uncomment for vi keys.
    # set -o vi
end
