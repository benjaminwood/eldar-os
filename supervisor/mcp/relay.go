package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// The relay: other agents, scripts and cron reach Ben through Eldar. An event
// goes to the supervisor (POST /relay), which holds it until Eldar's relay
// channel takes it; Eldar tells Ben in the chat, and carries his answer back
// with `supervisor tell-agent`.

type relayEvent struct {
	Text         string `json:"text"`
	Origin       string `json:"origin,omitempty"`
	SessionID    string `json:"sessionId,omitempty"`
	PaneID       string `json:"paneId,omitempty"`
	SessionName  string `json:"sessionName,omitempty"`
	Cwd          string `json:"cwd,omitempty"`
	ExpectsReply bool   `json:"expectsReply,omitempty"`
}

func (c *client) relay(ctx context.Context, ev relayEvent) error {
	out, err := c.post(ctx, "/relay", ev, 30*time.Second)
	if err != nil {
		return err
	}
	if e, ok := out["error"]; ok {
		return fmt.Errorf("%v", e)
	}
	return nil
}

type herdrPane struct {
	PaneID       string `json:"pane_id"`
	Title        string `json:"terminal_title_stripped"`
	AgentSession *struct {
		Value string `json:"value"`
	} `json:"agent_session"`
}

func (p herdrPane) session() string {
	if p.AgentSession == nil {
		return ""
	}
	return p.AgentSession.Value
}

func herdrPanes(ctx context.Context) ([]herdrPane, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "herdr", "pane", "list").Output()
	if err != nil {
		return nil, err
	}
	var r struct {
		Result struct {
			Panes []herdrPane `json:"panes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, err
	}
	return r.Result.Panes, nil
}

// notifyTool is notify_ben, in every session's supervisor MCP server.
func (s *server) notifyTool(ctx context.Context, args map[string]any) map[string]any {
	msg := str(args, "message")
	if strings.TrimSpace(msg) == "" {
		return errorText("message is required")
	}
	ev := relayEvent{Text: msg, Origin: str(args, "origin"), SessionID: s.c.sessionID, PaneID: os.Getenv("HERDR_PANE_ID"), Cwd: cwd(args)}
	ev.ExpectsReply, _ = args["expects_reply"].(bool)
	if ev.PaneID != "" {
		if panes, err := herdrPanes(ctx); err == nil {
			for _, p := range panes {
				if p.PaneID == ev.PaneID {
					ev.SessionName = p.Title
					if ev.SessionID == "" {
						ev.SessionID = p.session()
					}
				}
			}
		}
	}
	if ev.Origin == "" {
		ev.Origin = ev.SessionName
	}
	if ev.Origin == "" {
		ev.Origin = "notify_ben"
	}
	if err := s.c.relay(ctx, ev); err != nil {
		return errorText("Couldn't reach Eldar through the supervisor: " + err.Error())
	}
	note := ""
	if ev.ExpectsReply {
		if ev.SessionID != "" && ev.PaneID != "" {
			note = ` His answer will arrive in this session as a message starting "[Ben, via Eldar]".`
		} else {
			note = " This session isn't in a herdr pane, so Eldar can't type his answer back here."
		}
	}
	return textResult("Delivered; Eldar will tell Ben." + note)
}

const notifyDescription = "Message Ben through Eldar, his chief-of-staff agent. Eldar tells him in his chat (a push to his phone) and, " +
	"when you set expects_reply, types his answer back into this session as a message starting \"[Ben, via Eldar]\". " +
	"Treat that message as Ben's own reply.\n\n" +
	"USE IT when Ben would want to know now while he's away from the terminal: you're blocked on a decision only he " +
	"can make, a long task finished, a deploy or build went green or red, or he asked to be notified.\n\n" +
	"DON'T overuse it: an unneeded ping costs his attention. Skip routine progress, anything he's clearly watching " +
	"live, and trivial completions. One or two lines, leading with what he'd act on (\"deploy failed: 2 auth tests\" " +
	"beats \"task done\"). For a question, say what you need decided and the options.\n\n" +
	"After asking with expects_reply, carry on with anything that doesn't depend on the answer, or stop and wait: " +
	"his answer arrives as your next message. If the event can't be delivered right away (Eldar restarting), the " +
	"supervisor holds it until Eldar is back."

// Notify is `supervisor notify`: a one-way event to Eldar from a script or
// cron (they can't be answered back).
func Notify(origin, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("empty message")
	}
	if origin == "" {
		h, _ := os.Hostname()
		origin = fmt.Sprintf("%s:%d", h, os.Getppid())
	}
	return newClient().relay(context.Background(), relayEvent{Text: text, Origin: origin})
}

// TellAgent is `supervisor tell-agent`: Eldar passes Ben's answer to another
// Claude session by typing "[Ben, via Eldar] <message>" into its herdr pane.
// It checks the pane still holds that session, following it if it moved;
// `herdr agent prompt` refuses an agent sitting at an approval or question
// prompt, so this never answers a dialog.
func TellAgent(pane, session, msg string) (string, error) {
	if strings.TrimSpace(msg) == "" {
		return "", errors.New("empty message")
	}
	ctx := context.Background()
	panes, err := herdrPanes(ctx)
	if err != nil {
		return "", fmt.Errorf("herdr pane list: %w", err)
	}
	at, found := "", ""
	for _, p := range panes {
		if p.PaneID == pane {
			at = p.session()
		}
		if p.session() == session && found == "" {
			found = p.PaneID
		}
	}
	if at != session {
		if found == "" {
			if at == "" {
				at = "no Claude session"
			}
			return "", fmt.Errorf("session %s isn't running in any herdr pane (pane %s holds %s)", session, pane, at)
		}
		pane = found
	}
	out, err := exec.Command("herdr", "agent", "prompt", pane, "[Ben, via Eldar] "+msg).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("herdr agent prompt: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return fmt.Sprintf("delivered to %s (session %s)", pane, session), nil
}
