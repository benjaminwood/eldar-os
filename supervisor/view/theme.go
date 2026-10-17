package view

import (
	"fmt"
	"strings"
)

// Theme is Tokyo Night (rojadesign/iTerm2-Tokyo-Night-Color-Theme), named by
// role. The TUI uses it directly; the web UI gets the same values as CSS
// custom properties via CSSVars, so both surfaces look like one product.
var Theme = struct {
	Bg, Surface, Raised, Overlay, Border     string
	Fg, Bright, Dim, Faint                   string
	Accent, Violet, Deep, Lavender, SelectFg string
	Cyan, Teal, Mint                         string
	Red, Yellow, Orange                      string
}{
	Bg:       "#1a1b27", // Background
	Surface:  "#1e202f", // Cursor Guide
	Raised:   "#1e2336", // Tab
	Overlay:  "#2a2734", // Ansi 0
	Border:   "#454159", // Ansi 8 / Selection
	Fg:       "#bbc6f6", // Foreground
	Bright:   "#eeebff", // Ansi 15
	Dim:      "#7d7a9c", // Fg toward Border, for secondary text
	Faint:    "#5a5675",
	Accent:   "#b874fe", // Bold
	Violet:   "#7b64c3", // Ansi 12
	Deep:     "#5334b2", // Ansi 4
	Lavender: "#9b84ee", // Ansi 7
	SelectFg: "#c4b5ff", // Selected Text
	Cyan:     "#24d5f8", // Ansi 14
	Teal:     "#00bcdb", // Ansi 2
	Mint:     "#4ef4df", // Ansi 10
	Red:      "#e2514e", // Ansi 1
	Yellow:   "#e1c381", // Ansi 3
	Orange:   "#e69f51", // Ansi 5
}

// CSSVars renders the theme as `--name: value;` declarations.
func CSSVars() string {
	pairs := [][2]string{
		{"bg", Theme.Bg}, {"surface", Theme.Surface}, {"raised", Theme.Raised},
		{"overlay", Theme.Overlay}, {"border", Theme.Border}, {"fg", Theme.Fg},
		{"bright", Theme.Bright}, {"dim", Theme.Dim}, {"faint", Theme.Faint},
		{"accent", Theme.Accent}, {"violet", Theme.Violet}, {"deep", Theme.Deep},
		{"lavender", Theme.Lavender}, {"select-fg", Theme.SelectFg},
		{"cyan", Theme.Cyan}, {"teal", Theme.Teal}, {"mint", Theme.Mint},
		{"red", Theme.Red}, {"yellow", Theme.Yellow}, {"orange", Theme.Orange},
	}
	var b strings.Builder
	for _, p := range pairs {
		fmt.Fprintf(&b, "--%s:%s;", p[0], p[1])
	}
	return b.String()
}
