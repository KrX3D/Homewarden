// Package notify sends notifications (Discord, ntfy, Telegram, Gotify, email,
// webhook) for events the user picked per channel.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Config is a channel's settings; which fields apply depends on the type.
type Config struct {
	URL      string `json:"url,omitempty"`      // discord/webhook URL, ntfy/gotify server
	Topic    string `json:"topic,omitempty"`    // ntfy
	Token    string `json:"token,omitempty"`    // ntfy access token, gotify app token, telegram bot token
	ChatID   string `json:"chatId,omitempty"`   // telegram
	Host     string `json:"host,omitempty"`     // smtp
	Port     int    `json:"port,omitempty"`     // smtp
	Username string `json:"username,omitempty"` // smtp
	Password string `json:"password,omitempty"` // smtp
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
}

// Message is one notification.
type Message struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Priority int    `json:"priority"` // 0 normal, 1 high
	Event    string `json:"event"`
	Link     string `json:"link,omitempty"`
}

var client = &http.Client{Timeout: 20 * time.Second}

func postJSON(ctx context.Context, url string, v any, headers map[string]string) error {
	b, _ := json.Marshal(v)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, val := range headers {
		req.Header.Set(k, val)
	}
	return do(req)
}

func do(req *http.Request) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// Send delivers a message through one channel.
func Send(ctx context.Context, typ string, c Config, m Message) error {
	text := m.Body
	if m.Link != "" {
		text += "\n" + m.Link
	}
	switch typ {
	case "discord":
		color := 0x2f56c9
		if m.Priority > 0 {
			color = 0xc2382f
		}
		return postJSON(ctx, c.URL, map[string]any{"username": "Homewarden",
			"embeds": []map[string]any{{"title": m.Title, "description": text, "color": color}}}, nil)
	case "ntfy":
		if c.URL == "" {
			c.URL = "https://ntfy.sh"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/"+c.Topic, strings.NewReader(text))
		if err != nil {
			return err
		}
		req.Header.Set("Title", m.Title)
		req.Header.Set("Tags", "house")
		if m.Priority > 0 {
			req.Header.Set("Priority", "high")
		}
		if m.Link != "" {
			req.Header.Set("Click", m.Link)
		}
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		return do(req)
	case "gotify":
		prio := 5
		if m.Priority > 0 {
			prio = 8
		}
		return postJSON(ctx, strings.TrimRight(c.URL, "/")+"/message", map[string]any{"title": m.Title, "message": text, "priority": prio},
			map[string]string{"X-Gotify-Key": c.Token})
	case "telegram":
		return postJSON(ctx, "https://api.telegram.org/bot"+c.Token+"/sendMessage",
			map[string]any{"chat_id": c.ChatID, "text": m.Title + "\n" + text, "disable_web_page_preview": true}, nil)
	case "webhook":
		return postJSON(ctx, c.URL, m, nil)
	case "email":
		return sendMail(c, m.Title, text)
	}
	return fmt.Errorf("unknown channel type %q", typ)
}

func sendMail(c Config, subject, body string) error {
	if c.Host == "" || c.From == "" || c.To == "" {
		return errors.New("email needs server, from and to")
	}
	if c.Port == 0 {
		c.Port = 587
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	msg := "From: " + c.From + "\r\nTo: " + c.To + "\r\nSubject: " + subject +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + strings.ReplaceAll(body, "\n", "\r\n")
	var auth smtp.Auth
	if c.Username != "" {
		auth = smtp.PlainAuth("", c.Username, c.Password, c.Host)
	}
	return smtp.SendMail(addr, auth, c.From, strings.Split(c.To, ","), []byte(msg))
}
