package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/coder/websocket"
)

// terminal stands in for /admin/terminal, which in the real supervisor is
// `herdr terminal attach` on a pane in Eldar's VM. It draws a made-up Claude
// Code session (the billing-retries fix) laid out for the size the browser
// asks for, and redraws it on resize and as the story moves on: the push
// waits on approval #41; once it's through, the session opens a PR and asks
// for a log check, which it reports once that's approved.
func (d *demo) terminal(w http.ResponseWriter, r *http.Request) {
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	changed, unsub := d.subscribe()
	defer unsub()
	var mu sync.Mutex
	drawn := ""
	draw := func() error {
		mu.Lock()
		defer mu.Unlock()
		stage, check := d.story()
		screen := session(max(cols, 30), max(rows, 12), stage, check)
		if screen == drawn {
			return nil
		}
		drawn = screen
		return ws.Write(ctx, websocket.MessageBinary, []byte(screen))
	}
	if draw() != nil {
		return
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-changed:
				if draw() != nil {
					cancel()
					return
				}
			}
		}
	}()
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var msg struct {
			Type       string
			Cols, Rows int
		}
		if typ == websocket.MessageText && json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
			mu.Lock()
			cols, rows = msg.Cols, msg.Rows
			mu.Unlock()
			if draw() != nil {
				return
			}
		}
	}
}

func rgb(r, g, b int) string {
	return "\x1b[38;2;" + strconv.Itoa(r) + ";" + strconv.Itoa(g) + ";" + strconv.Itoa(b) + "m"
}
func bg(r, g, b int) string {
	return "\x1b[48;2;" + strconv.Itoa(r) + ";" + strconv.Itoa(g) + ";" + strconv.Itoa(b) + "m"
}

const (
	reset = "\x1b[0m"
	bold  = "\x1b[1m"
)

var (
	dim    = rgb(153, 153, 153)
	green  = rgb(78, 186, 101)
	white  = rgb(255, 255, 255)
	orange = rgb(215, 119, 87)
	border = rgb(136, 136, 136)
	purple = rgb(175, 135, 255)
	redBg  = bg(92, 32, 40)
	grnBg  = bg(30, 80, 46)
)

// wrap breaks text into lines of at most width runes, on spaces.
func wrap(text string, width int) []string {
	var out []string
	line := ""
	for _, w := range strings.Fields(text) {
		switch {
		case line == "":
			line = w
		case utf8.RuneCountInString(line)+1+utf8.RuneCountInString(w) <= width:
			line += " " + w
		default:
			out = append(out, line)
			line = w
		}
	}
	return append(out, line)
}

func trunc(s string, width int) string {
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	return string([]rune(s)[:max(width-1, 0)]) + "…"
}

// session renders the screen: the conversation, scrolled so its end sits
// above the spinner and the prompt box, as Claude Code draws it.
func session(cols, rows, stage, checkID int) string {
	var lines []string
	add := func(l ...string) { lines = append(lines, l...) }
	// para: a first-line marker (marker cells wide) and wrapped text, the rest
	// indented to line up.
	para := func(marker string, markerW int, style, text string) {
		for i, l := range wrap(text, cols-markerW-1) {
			if i == 0 {
				add(marker + style + l + reset)
			} else {
				add(strings.Repeat(" ", markerW) + style + l + reset)
			}
		}
	}
	say := func(text string) { para(white+"⏺ "+reset, 2, "", text); add("") }
	tool := func(name, args string) {
		para(green+"⏺ "+reset, 2, "", bold+name+reset+"("+args+")")
	}
	out := func(first bool, style, text string) {
		lead := "     "
		if first {
			lead = "  " + dim + "⎿" + reset + "  "
		}
		add(lead + style + trunc(text, cols-6) + reset)
	}

	para(dim+"> "+reset, 2, dim, "Renewals are supposed to retry failed charges, but about 2% since yesterday never get a second attempt. Find out why and fix it.")
	add("")
	say("I'll start with the retry job's errors from the last hour.")
	tool("supervisor - supervisor_exec (MCP)", `command: "aws logs tail /ecs/api --since 1h --filter-pattern ERROR", aws_profile: "staging.ReadOnly"`)
	out(true, "", "ERROR retry: skip renewal 8812: next_attempt_at is in the past")
	out(false, "", "ERROR retry: skip renewal 8815: next_attempt_at is in the past")
	out(false, dim, "… +41 lines (ctrl+r to expand)")
	add("")
	tool("Read", "app/jobs/retry_failed_charges.rb")
	out(true, "", "Read 64 lines")
	add("")
	say("Found it. A charge that fails late in the day gets its next attempt scheduled for 9:00 that same day in the account's time zone, which is already past, so the scheduler skips it.")
	tool("Update", "app/jobs/retry_failed_charges.rb")
	out(true, "", "Updated retry_failed_charges.rb: 2 additions, 1 removal")
	diff := func(n, sign, bgc, code string) {
		add("     " + dim + n + reset + " " + bgc + trunc(sign+" "+code, cols-11) + reset)
	}
	diff("41", "-", redBg, "next_at = Time.zone.now.change(hour: 9) + delay")
	diff("41", "+", grnBg, "base = [Time.current, failed_at].max")
	diff("42", "+", grnBg, "next_at = base + delay")
	add("")
	tool("Bash", "bin/rspec spec/jobs/retry_failed_charges_spec.rb")
	out(true, "", "14 examples, 0 failures")
	add("")
	say("Tests pass, including a new one for late-day failures. Pushing the branch so you can review it.")
	tool("supervisor - supervisor_exec (MCP)", `command: "git push origin fix/billing-retries", credentials: ["GITHUB_TOKEN"]`)
	spinner := "Waiting for approval… "
	if stage == 0 {
		out(true, dim, "Waiting for Ben's approval (#41)…")
	} else {
		out(true, "", "To github.com:example/api.git")
		out(false, "", " * [new branch]  fix/billing-retries -> fix/billing-retries")
		add("")
		tool("supervisor - supervisor_exec (MCP)", `command: "gh pr create --fill", credentials: ["GITHUB_TOKEN"]`)
		out(true, "", "https://github.com/example/api/pull/87")
		add("")
		say("PR #87 is up. Counting how many renewals were skipped, for the PR description.")
		tool("supervisor - supervisor_exec (MCP)", `command: "aws logs tail /ecs/api --since 24h --filter-pattern \"skip renewal\"", aws_profile: "staging.ReadOnly"`)
		if stage == 1 {
			out(true, dim, fmt.Sprintf("Waiting for Ben's approval (#%d)…", checkID))
		} else {
			out(true, "", "ERROR retry: skip renewal 8812: next_attempt_at is in the past")
			out(false, dim, "… +311 lines (ctrl+r to expand)")
			add("")
			say("312 renewals were skipped in the last day. That's in the PR description now; they'll retry on the next run once this merges.")
			spinner = ""
		}
	}

	// The bottom: spinner, prompt box, mode line.
	bar := strings.Repeat("─", cols-2)
	bottom := []string{
		"",
		"",
		"",
		border + "╭" + bar + "╮" + reset,
		border + "│" + reset + " > " + strings.Repeat(" ", cols-5) + border + "│" + reset,
		border + "╰" + bar + "╯" + reset,
		"  " + purple + "⏵⏵ accept edits on" + reset + dim + " (shift+tab to cycle)" + reset,
	}
	if spinner != "" {
		bottom[1] = orange + "✻ " + spinner + reset + dim + trunc("(2m 39s · esc to interrupt)", cols-len([]rune(spinner))-2) + reset
	}
	if keep := rows - len(bottom); len(lines) > keep {
		lines = lines[len(lines)-keep:]
	} else {
		for len(lines) < keep {
			lines = append(lines, "")
		}
	}
	return "\x1b[2J\x1b[H\x1b[?25l" + strings.Join(append(lines, bottom...), "\r\n")
}
