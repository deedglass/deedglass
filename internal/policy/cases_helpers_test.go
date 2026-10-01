package policy

import (
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
)

type refVal = ref.Val

func celString(s string) ref.Val { return types.String(s) }
func celInt(i int64) ref.Val     { return types.Int(i) }
