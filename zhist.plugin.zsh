# zhist plugin entry point for zsh plugin managers (zinit, zsh-snap, ...).
# The zhist binary must be installed separately; see the README.
(( $+commands[zhist] )) && eval "$(zhist init)"
