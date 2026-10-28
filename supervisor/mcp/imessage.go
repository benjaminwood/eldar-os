package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The imessage channel: iMessages to an agent's own Apple ID, through the
// supervisor's BlueBubbles bridge (server/imessage.go). Messages from allowed
// contacts arrive as <channel source="imessage" …>; reply, history and
// contacts reach only those contacts.

const imessageInstructions = `People write to you by iMessage. Their messages arrive as <channel source="imessage" from="..." name="..." chat="..." message_id="..." ts="...">: from is the sender's phone number or Apple ID, name is who they are, chat is the conversation. Only allowed chats reach you; the supervisor drops everything else.

A group chat message also has group="true", members="..." (everyone in it besides you) and, if it has one, chat_name. In a group, answer to the chat, not to the sender alone, and only speak when you're addressed or have something worth adding; stay out of conversation between the others.

They read iMessage, not this terminal: anything you want them to see must go through the reply tool, to the chat attribute of the message you're answering. Your transcript output never reaches them.

Write like a person texting: plain text (iMessage shows Markdown symbols literally), short, one message per answer rather than a stream of fragments. Don't send a half-finished thought; when a task takes a while, say so in one line first only if it really will take long.

attachments="N" means the message had N attachments (photos, files) you can't see; say so if it matters.

In a direct chat they see you typing as soon as their message arrives, until you reply or react, so answer every direct message one way or the other. In a group, call typing yourself when you're going to answer and it will take more than a few seconds; your reply clears it. Don't show typing for messages you won't answer.

To react the way a person taps a heart or a thumbs up, use the react tool with the message's message_id; a reaction can stand in for a reply when that's all the message needs.

Use the history tool when you need earlier conversation you no longer have (after a restart, or a message that refers back), and contacts to see whom you can write to.`

func (ch *channel) imessageServer(version string) *server {
	reply := tool{Name: "reply",
		Description: "Send an iMessage in an allowed chat: usually the chat of the message you're answering. Plain text.",
		Schema: func([]string, string) map[string]any {
			return object(map[string]any{
				"to":   map[string]any{"type": "string", "description": "A message's chat attribute, or a contact's phone number or Apple ID email for a direct chat."},
				"text": map[string]any{"type": "string", "description": "The message, plain text."},
			}, "to", "text")
		},
		run: func(ctx context.Context, args map[string]any) map[string]any {
			to, text := str(args, "to"), str(args, "text")
			if strings.TrimSpace(to) == "" || strings.TrimSpace(text) == "" {
				return errorText("to and text are required")
			}
			out, err := ch.call(ctx, http.MethodPost, "/imessage/send", map[string]any{"to": to, "text": text})
			if err != nil {
				return errorText(err.Error())
			}
			return textResult(fmt.Sprintf("Sent to %v.", out["name"]))
		}}
	history := tool{Name: "history",
		Description: "Recent iMessages in one allowed chat, oldest first (from: \"me\" or the sender's name).",
		Schema: func([]string, string) map[string]any {
			return object(map[string]any{
				"with":  map[string]any{"type": "string", "description": "A chat attribute, or a contact's phone number or Apple ID email."},
				"limit": map[string]any{"type": "number", "description": "How many of the latest messages, 1-100. Default 20."},
			}, "with")
		},
		run: func(ctx context.Context, args map[string]any) map[string]any {
			limit := 20
			if l, ok := args["limit"].(float64); ok {
				limit = min(max(int(l), 1), 100)
			}
			q := url.Values{"with": {str(args, "with")}, "limit": {fmt.Sprint(limit)}}
			out, err := ch.call(ctx, http.MethodGet, "/imessage/history?"+q.Encode(), nil)
			if err != nil {
				return errorText(err.Error())
			}
			msgs, _ := out["messages"].([]any)
			var lines []string
			for _, x := range msgs {
				m, _ := x.(map[string]any)
				ts, _ := m["ts"].(float64)
				line := fmt.Sprintf("%s %v: %v", time.UnixMilli(int64(ts)).UTC().Format(time.RFC3339), m["from"], m["text"])
				if n, _ := m["attachments"].(float64); n > 0 {
					line += fmt.Sprintf(" [%d attachment(s)]", int(n))
				}
				lines = append(lines, line)
			}
			if len(lines) == 0 {
				return textResult("(no messages)")
			}
			return textResult(strings.Join(lines, "\n\n"))
		}}
	contacts := tool{Name: "contacts",
		Description: "The people you can exchange iMessages with (some only in a group chat).",
		Schema:      func([]string, string) map[string]any { return object(map[string]any{}) },
		run: func(ctx context.Context, _ map[string]any) map[string]any {
			out, err := ch.call(ctx, http.MethodGet, "/imessage/contacts", nil)
			if err != nil {
				return errorText(err.Error())
			}
			cs, _ := out["contacts"].([]any)
			var lines []string
			for _, x := range cs {
				c, _ := x.(map[string]any)
				line := fmt.Sprintf("%v: %v", c["name"], c["address"])
				if d, _ := c["direct"].(bool); !d {
					line += " (only in a group chat with a direct contact)"
				}
				lines = append(lines, line)
			}
			if len(lines) == 0 {
				return textResult("(no contacts)")
			}
			return textResult(strings.Join(lines, "\n"))
		}}
	react := tool{Name: "react",
		Description: "React to a message with an iMessage tapback (heart, thumbs up, ...), in its chat. Use it where a person would tap a reaction rather than type a reply.",
		Schema: func([]string, string) map[string]any {
			return object(map[string]any{
				"message_id": map[string]any{"type": "string", "description": "The message_id attribute of the message you're reacting to."},
				"reaction": map[string]any{"type": "string", "enum": []string{"love", "like", "dislike", "laugh", "emphasize", "question"},
					"description": "love (heart), like (thumbs up), dislike (thumbs down), laugh (haha), emphasize (!!), question (?)."},
				"remove": map[string]any{"type": "boolean", "description": "Take back a reaction you gave earlier."},
			}, "message_id", "reaction")
		},
		run: func(ctx context.Context, args map[string]any) map[string]any {
			id, err := strconv.Atoi(strings.TrimSpace(str(args, "message_id")))
			if err != nil {
				return errorText("message_id must be a message's message_id attribute")
			}
			remove, _ := args["remove"].(bool)
			out, err := ch.call(ctx, http.MethodPost, "/imessage/react", map[string]any{"messageId": id, "reaction": str(args, "reaction"), "remove": remove})
			if err != nil {
				return errorText(err.Error())
			}
			return textResult(fmt.Sprintf("Reacted %v to %v's message.", out["reacted"], out["to"]))
		}}
	typing := tool{Name: "typing",
		Description: "Show \"typing…\" in a group chat while you work on an answer (direct chats show it automatically). Your reply or reaction clears it; it also clears itself after a minute.",
		Schema: func([]string, string) map[string]any {
			return object(map[string]any{
				"to":   map[string]any{"type": "string", "description": "The chat attribute of the message you're answering."},
				"stop": map[string]any{"type": "boolean", "description": "Clear it, when you decide not to reply after all."},
			}, "to")
		},
		run: func(ctx context.Context, args map[string]any) map[string]any {
			stop, _ := args["stop"].(bool)
			if _, err := ch.call(ctx, http.MethodPost, "/imessage/typing", map[string]any{"to": str(args, "to"), "on": !stop}); err != nil {
				return errorText(err.Error())
			}
			if stop {
				return textResult("Typing indicator cleared.")
			}
			return textResult("They see you typing.")
		}}
	tools := []tool{reply, react, typing, history, contacts}
	s := &server{c: ch.c, name: "imessage", version: version, instructions: imessageInstructions, channel: true, tools: map[string]tool{}}
	for _, t := range tools {
		s.tools[t.Name] = t
	}
	s.describe = func(context.Context) []map[string]any {
		var out []map[string]any
		for _, t := range tools {
			out = append(out, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Schema(nil, "")})
		}
		return out
	}
	s.start = func(ctx context.Context, notify func(string, any)) {
		ch.listen(ctx, "imessage", "/imessage/stream", "imessage", func(data map[string]any) {
			ts, _ := data["ts"].(float64)
			meta := map[string]any{"from": data["from"], "name": data["name"], "chat": data["chat"],
				"message_id": fmt.Sprint(data["id"]), "ts": time.UnixMilli(int64(ts)).UTC().Format(time.RFC3339)}
			if g, _ := data["group"].(bool); g {
				meta["group"] = "true"
				var names []string
				for _, n := range asSlice(data["members"]) {
					names = append(names, fmt.Sprint(n))
				}
				meta["members"] = strings.Join(names, ", ")
				if n, _ := data["chatName"].(string); n != "" {
					meta["chat_name"] = n
				}
			}
			if n, _ := data["attachments"].(float64); n > 0 {
				meta["attachments"] = fmt.Sprint(int(n))
			}
			notify("notifications/claude/channel", map[string]any{"content": data["text"], "meta": meta})
		})
	}
	return s
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}
