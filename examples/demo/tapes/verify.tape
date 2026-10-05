# Change one byte (S37 scene 7). After make demo: the export and the changed copy are in .tmp/demo/.
Source examples/demo/tapes/_brand.tape
Output verify.mp4
Source examples/demo/tapes/_setup.tape
Hide
Type "cd .tmp/demo && clear"
Enter
Show
Type "deedglass verify deeds.jsonl --keys keys.json"
Enter
Sleep 1.2s
Sleep 1.5s
Type "deedglass verify deeds-changed.jsonl --keys keys.json"
Enter
Sleep 1.2s
Sleep 2s
