package stripe

import (
	"regexp"
	"sync"
)

var (
	idMu  sync.Mutex
	idRes = map[string]*regexp.Regexp{}
)

func matchID(pat, id string) bool {
	idMu.Lock()
	re, ok := idRes[pat]
	if !ok {
		re = regexp.MustCompile(pat)
		idRes[pat] = re
	}
	idMu.Unlock()
	return re.MatchString(id)
}
