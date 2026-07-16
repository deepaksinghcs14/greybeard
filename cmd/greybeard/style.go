package main

import (
	"os"
	"strings"
)

// Minimal ANSI theming — no dependency. Colors mirror the visualize page:
// blue=imports, green=calls_api, magenta=shares_schema, grey=the beard.
var colorOn = func() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}()

func paint(code, s string) string {
	if !colorOn {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func bold(s string) string    { return paint("1", s) }
func dim(s string) string     { return paint("2", s) }
func grey(s string) string    { return paint("90", s) }
func green(s string) string   { return paint("32", s) }
func red(s string) string     { return paint("31", s) }
func blue(s string) string    { return paint("34", s) }
func magenta(s string) string { return paint("35", s) }

// glyph colors a leading ✓ (green) or ✗ (red) status line.
func glyph(line string) string {
	switch {
	case strings.HasPrefix(line, "✓"):
		return green("✓") + line[len("✓"):]
	case strings.HasPrefix(line, "✗"):
		return red("✗") + line[len("✗"):]
	}
	return line
}

// edgeColor themes an edge-type name with its graph color.
func edgeColor(t string) string {
	switch t {
	case "imports":
		return blue(t)
	case "calls_api":
		return green(t)
	case "shares_schema":
		return magenta(t)
	}
	return t
}

// banner is the old man himself. Shown on usage and version only.
func banner() string {
	art := []string{
		`      .-"""-.      `,
		`     / .===. \     `,
		`     \/ o o \/     `,
		`     ( \___/ )     `,
		`  ____)     (____  `,
		` '-.__ ~~~~~ __.-' `,
		`      '~~~~~'      `,
	}
	tag := []string{
		"",
		"",
		"   greybeard " + version,
		"   " + dim("he remembers what your repos forgot"),
		"",
		"",
		"",
	}
	var b strings.Builder
	for i, line := range art {
		b.WriteString(grey(line) + tag[i] + "\n")
	}
	return b.String()
}
