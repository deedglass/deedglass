# Put it back (S37 scene 5). After mismatch.tape: make demo-trigger-off; the undo waits for a
# person, who countersigns on the phone (make demo-lan) or in Slack while this records.
Source examples/demo/tapes/_brand.tape
Output undo.mp4
Source examples/demo/tapes/_setup.tape
Hide
Type "export DEEDGLASS_API_KEY=$(grep ^admin= examples/demo/.demo-keys | cut -d= -f2) A=$(deedglass actions list --org acme-demo --tool postgres.execute --limit 1 --json | grep -o 'act_[0-9a-z]*' | head -1) && clear"
Enter
Show
Type "deedglass undo preview $A"
Enter
Sleep 1.2s
Sleep 2s
Type "deedglass undo run $A --plan $(deedglass undo preview $A --json | grep -o 'sha256:[0-9a-f]*' | head -1) --reason 'Put the 300 extra orders back'"
Enter
Sleep 1.2s
Sleep 20s
