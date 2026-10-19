package connector

// PHIKeep says which parts of a tool's stored request and result are identifiers that PHI mode
// keeps as they are (HC-P09); everything else is stored as a digest once the action is sealed.
// Args names top-level arguments; Result is a list of internal/phi paths ("status",
// "headers.etag"). A connector that says nothing keeps nothing: argument names stay, every value
// and the whole result become digests.
type PHIKeep struct {
	Args   []string
	Result []string
}

// PHIKeeper is a connector type that names its identifier arguments and result paths.
type PHIKeeper interface {
	PHIKeep(inst *Instance, t ToolSpec) PHIKeep
}

// PHIKeeping is an Executor that can answer PHIKeep for its tools.
type PHIKeeping interface {
	PHIKeep(t ToolSpec) PHIKeep
}

// PHIKeep implements PHIKeeping through the bound connector, or keeps nothing.
func (b *bound) PHIKeep(t ToolSpec) PHIKeep {
	if k, ok := b.impl.(PHIKeeper); ok {
		return k.PHIKeep(b.inst, t)
	}
	return PHIKeep{}
}
