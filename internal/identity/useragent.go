package identity

import "strings"

// DescribeUserAgent turns a User-Agent header into the few words a sign-in email shows ("Chrome on
// macOS"). It knows Chrome, Safari, Firefox and Edge on macOS, Windows, iOS, Android and Linux;
// anything else is "a browser". It returns "" when it recognizes neither part. The raw header is
// never printed.
func DescribeUserAgent(ua string) string {
	browser := uaBrowser(ua)
	os := uaOS(ua)
	switch {
	case browser == "" && os == "":
		return ""
	case browser == "":
		return "a browser on " + os
	case os == "":
		return browser
	}
	return browser + " on " + os
}

func uaBrowser(ua string) string {
	switch {
	case strings.Contains(ua, "Edg/") || strings.Contains(ua, "EdgA/") || strings.Contains(ua, "EdgiOS/"):
		return "Edge"
	case strings.Contains(ua, "Firefox/") || strings.Contains(ua, "FxiOS/"):
		return "Firefox"
	case strings.Contains(ua, "OPR/") || strings.Contains(ua, "SamsungBrowser/"):
		return ""
	case strings.Contains(ua, "Chrome/") || strings.Contains(ua, "CriOS/"):
		return "Chrome"
	case strings.Contains(ua, "Safari/") && strings.Contains(ua, "Version/"):
		return "Safari"
	}
	return ""
}

func uaOS(ua string) string {
	switch {
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad") || strings.Contains(ua, "iPod"):
		return "iOS"
	case strings.Contains(ua, "Android"):
		return "Android"
	case strings.Contains(ua, "Mac OS X") || strings.Contains(ua, "Macintosh"):
		return "macOS"
	case strings.Contains(ua, "Windows"):
		return "Windows"
	case strings.Contains(ua, "Linux") || strings.Contains(ua, "X11"):
		return "Linux"
	}
	return ""
}
