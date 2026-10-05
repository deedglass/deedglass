# The DROP that didn't happen (S37 scene 2). make demo-reset first.
Source examples/demo/tapes/_brand.tape
Output drop.mp4
Source examples/demo/tapes/_setup.tape
Type "deedglass act postgres.execute --args '{\"sql\":\"DROP TABLE public.refunds\"}'"
Sleep 500ms
Enter
Sleep 1.2s
Sleep 2s
