package stripe

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
)

// Form flattens params into Stripe's bracket syntax: {"metadata": {"a": "b"}} becomes
// metadata[a]=b, a list becomes k[0], k[1]. Values are strings, int64 (never a float: money is
// integer minor units), bool, map[string]any and []any; anything else is an error, so a float
// can't reach Stripe by accident. The result is deterministic (url.Values.Encode sorts keys).
func Form(params map[string]any) (url.Values, error) {
	out := url.Values{}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := flatten(out, k, params[k], 0); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func flatten(out url.Values, key string, v any, depth int) error {
	if depth > 4 {
		return fmt.Errorf("stripe: %s is nested too deep", key)
	}
	switch x := v.(type) {
	case string:
		out.Add(key, x)
	case int64:
		out.Add(key, strconv.FormatInt(x, 10))
	case int:
		out.Add(key, strconv.Itoa(x))
	case bool:
		out.Add(key, strconv.FormatBool(x))
	case map[string]string:
		m := make(map[string]any, len(x))
		for k, s := range x {
			m[k] = s
		}
		return flatten(out, key, m, depth)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := flatten(out, key+"["+k+"]", x[k], depth+1); err != nil {
				return err
			}
		}
	case []any:
		for i, e := range x {
			if err := flatten(out, key+"["+strconv.Itoa(i)+"]", e, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("stripe: %s has a %T, which isn't sent to Stripe", key, v)
	}
	return nil
}
