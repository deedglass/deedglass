package rest

import (
	"sort"

	"github.com/deedglass/deedglass/internal/connector"
)

var _ connector.PHIKeeper = (*Connector)(nil)

// phiResult are the result paths PHI mode keeps for every REST tool: the HTTP status and the
// identifier headers (where the server put the resource and which version it is). The body is
// always a digest.
var phiResult = []string{"status", "headers.content-type", "headers.etag", "headers.last-modified", "headers.location",
	"headers.request-id", "headers.x-request-id"}

// PHIKeep implements connector.PHIKeeper (HC-P09): a tool's path args are identifiers (they are
// already limited to letters, digits and ._~-, and name the resource the call reaches); body, query
// and header args are stored as digests.
func (c *Connector) PHIKeep(inst *connector.Instance, t connector.ToolSpec) connector.PHIKeep {
	keep := connector.PHIKeep{Result: append([]string(nil), phiResult...)}
	st, err := parse(inst)
	if err != nil {
		return keep
	}
	tool, ok := st.tools[t.Name]
	if !ok {
		return keep
	}
	for name, a := range tool.Args {
		if a.In == "path" && !a.Sensitive {
			keep.Args = append(keep.Args, name)
		}
	}
	sort.Strings(keep.Args)
	return keep
}
