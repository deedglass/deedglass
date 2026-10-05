# Change one byte, vertical (the Shorts and Reels cut, S37 §3.5). After make demo.
Source examples/demo/tapes/_brand-vertical.tape
Output verify-vertical.mp4
Source examples/demo/tapes/_setup.tape
Hide
Type "cd .tmp/demo && cp deeds.jsonl d.jsonl && cp deeds-changed.jsonl x.jsonl && cp keys.json k.json && clear"
Enter
Show
Type "deedglass verify d.jsonl --keys k.json"
Enter
Sleep 1.2s
Sleep 1.5s
Type "deedglass verify x.jsonl --keys k.json"
Enter
Sleep 1.2s
Sleep 2s
