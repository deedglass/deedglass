// Command mcpbgen writes the Claude Desktop bundles (S47 §3.6) from built binaries:
//
//	go run ./cmd/mcpbgen -dist dist                     # after goreleaser: dist/deedglass_<goos>_<goarch>*/deedglass[.exe]
//	go run ./cmd/mcpbgen -bin darwin-arm64=bin/deedglass # one platform from any binary
//
// Bundles go to -out (default dist/mcpb) as deedglass-<version>-<platform>.mcpb. scripts/mcpb.sh
// builds the binaries, runs this and checks each manifest with the pinned @anthropic-ai/mcpb CLI.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/deedglass/deedglass/internal/installlinks"
	"github.com/deedglass/deedglass/internal/mcpb"
)

type bins map[string]string

func (b bins) String() string { return "" }
func (b bins) Set(v string) error {
	id, path, ok := strings.Cut(v, "=")
	if _, known := mcpb.ByID(id); !ok || !known {
		return fmt.Errorf("-bin takes <platform>=<path>, platform one of darwin-arm64, darwin-x64, win32-x64")
	}
	b[id] = path
	return nil
}

func main() {
	b := bins{}
	dist := flag.String("dist", "", "GoReleaser's dist directory")
	out := flag.String("out", "dist/mcpb", "where to write the bundles")
	icon := flag.String("icon", "integrations/cursor/icon-512.png", "the icon (PNG); empty for none")
	version := flag.String("version", installlinks.Version, "the bundle version")
	flag.Var(b, "bin", "<platform>=<path to binary> (repeatable)")
	flag.Parse()
	if err := run(*dist, *out, *icon, *version, b); err != nil {
		fmt.Fprintln(os.Stderr, "mcpbgen:", err)
		os.Exit(1)
	}
}

func run(dist, out, iconPath, version string, b bins) error {
	if dist != "" {
		for _, p := range mcpb.Platforms {
			exe := "deedglass"
			if p.GoOS == "windows" {
				exe += ".exe"
			}
			m, _ := filepath.Glob(filepath.Join(dist, "deedglass_"+p.GoOS+"_"+p.GoArch+"*", exe))
			if len(m) != 1 {
				return fmt.Errorf("expected one %s/%s binary in %s, found %d", p.GoOS, p.GoArch, dist, len(m))
			}
			b[p.ID] = m[0]
		}
	}
	if len(b) == 0 {
		return fmt.Errorf("nothing to bundle: pass -dist or -bin")
	}
	var icon []byte
	if iconPath != "" {
		var err error
		if icon, err = os.ReadFile(iconPath); err != nil {
			return err
		}
		if !bytes.HasPrefix(icon, []byte("\x89PNG\r\n\x1a\n")) {
			return fmt.Errorf("%s isn't a PNG", iconPath)
		}
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	for _, p := range mcpb.Platforms {
		path, ok := b[p.ID]
		if !ok {
			continue
		}
		bin, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := mcpb.Write(&buf, p, version, bin, icon); err != nil {
			return err
		}
		dst := filepath.Join(out, p.FileName(version))
		if err := os.WriteFile(dst, buf.Bytes(), 0o644); err != nil {
			return err
		}
		fmt.Printf("%s  %.1f MB\n", dst, float64(buf.Len())/1e6)
	}
	return nil
}
