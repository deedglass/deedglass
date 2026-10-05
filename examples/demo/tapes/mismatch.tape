# One row reported, 301 committed (S37 scene 4). make demo-reset && make demo-trigger-on first.
Source examples/demo/tapes/_brand.tape
Output mismatch.mp4
Source examples/demo/tapes/_setup.tape
Type "deedglass act postgres.execute --args '{\"sql\":\"UPDATE public.orders SET status = ''shipped'' WHERE id = 42\"}'"
Sleep 500ms
Enter
Sleep 1.2s
Sleep 3s
