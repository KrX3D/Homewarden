// Package notify sends notifications (Discord, ntfy, Telegram, Gotify, email,
// webhook) for events the user picked per channel.
package notify

import "github.com/KrX3D/Homewarden/internal/notify/channel"

// Config, Message and Send live in package channel (shared with the agent).
type (
	Config  = channel.Config
	Message = channel.Message
)

// Send delivers a message through one channel.
var Send = channel.Send
