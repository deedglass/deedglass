# Hidden setup shared by the demo tapes: the demo's server and the claude-code agent's key from
# examples/demo/.demo-keys (never shown on screen), bin/deedglass on the PATH and a plain prompt.
# Then one visible frame, printed (not typed), naming the commit the recording came from.
Hide
Type "cd ~/txv/product && export PATH=$PWD/bin:$PATH PS1='$ ' DEEDGLASS_SERVER=http://127.0.0.1:8091 DEEDGLASS_API_KEY=$(grep ^claude-code= examples/demo/.demo-keys | cut -d= -f2) && clear && echo '# deedglass commit '$(git rev-parse --short HEAD)"
Enter
Show
Sleep 1.5s
Hide
Type "clear"
Enter
Show
