package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	slackexample "github.com/deedglass/deedglass/examples/slack"
)

// deedglass slack manifest (S29): the Slack app manifest with this server's interactivity URL.

var slackManifestValues struct {
	baseURL, name string
}

func slackManifestFlags(fs *flag.FlagSet) {
	v := &slackManifestValues
	v.baseURL, v.name = "", ""
	fs.StringVar(&v.baseURL, "base-url", "", "your Deedglass server's public HTTPS URL, like https://deedglass.example.com")
	fs.StringVar(&v.name, "name", "Deedglass", "the app's name in Slack (at most 35 characters)")
}

// slackManifest is the manifest object of --json.
type slackManifest struct {
	Object     string `json:"object"` // slack_manifest
	RequestURL string `json:"request_url"`
	Name       string `json:"name"`
	Manifest   string `json:"manifest"`
}

// buildSlackManifest fills the request URL and the app's name into the example manifest.
func buildSlackManifest(base, name string) (slackManifest, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		strings.Trim(u.Path, "/") != "" {
		return slackManifest{}, errors.New("--base-url must be your server's public https:// address with no path, like https://deedglass.example.com")
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 35 || strings.ContainsAny(name, "\"\\\n\r\t") {
		return slackManifest{}, errors.New("--name must be 1 to 35 characters, without quotes or line breaks")
	}
	req := "https://" + u.Host + "/v1/slack/interact"
	m := strings.Replace(slackexample.Manifest, slackexample.Placeholder, req, 1)
	m = strings.Replace(m, "  name: Deedglass\n", "  name: "+name+"\n", 1)
	// The comment lines are for the example file; the printed manifest is what Slack reads.
	var out []string
	for _, line := range strings.Split(m, "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return slackManifest{Object: "slack_manifest", RequestURL: req, Name: name, Manifest: strings.TrimLeft(strings.Join(out, "\n"), "\n")}, nil
}

func runSlackManifest(_ context.Context, e *Env, args []string) error {
	if len(args) > 0 {
		return usageErr(fmt.Errorf("deedglass slack manifest takes no arguments (got %q)", args[0]),
			"deedglass slack manifest --base-url https://your-host")
	}
	v := slackManifestValues
	if v.baseURL == "" {
		return usageErr(errors.New("--base-url is required"),
			"Pass your server's public HTTPS URL, for example: deedglass slack manifest --base-url https://abc.trycloudflare.com")
	}
	m, err := buildSlackManifest(v.baseURL, v.name)
	if err != nil {
		return usageErr(err, "deedglass slack manifest --base-url https://your-host [--name Deedglass]")
	}
	if e.Globals.JSON {
		return writeJSON(e, m)
	}
	fmt.Fprint(e.Stdout, m.Manifest)
	return nil
}
