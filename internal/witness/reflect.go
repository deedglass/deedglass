package witness

import "reflect"

// anyType is the target type for ref.Val.ConvertToNative when a diff shows a CEL value.
var anyType = reflect.TypeFor[any]()
