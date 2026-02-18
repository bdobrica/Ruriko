// Package matrix wraps mautrix-go for the Gitai agent runtime.
//
// Unlike Ruriko's Matrix client (which joins admin rooms and routes commands),
// Gitai's client joins the rooms listed in the Gosuto trust.allowedRooms and
// delivers every message it receives to a MessageHandler for turn processing.
package matrix

import (
"context"
"fmt"
"log/slog"
"time"

"maunium.net/go/mautrix"
"maunium.net/go/mautrix/event"
"maunium.net/go/mautrix/id"
)

// Config holds the Matrix connection parameters for the agent.
type Config struct {
Homeserver  string
UserID      string
AccessToken string
}

// MessageHandler is called for each incoming text message.
type MessageHandler func(ctx context.Context, evt *event.Event)

// Client is the agent-side Matrix client.
type Client struct {
mxc    *mautrix.Client
cfg    *Config
stopCh chan struct{}
}

// New creates a Matrix client for the agent but does not start syncing yet.
func New(cfg *Config) (*Client, error) {
mxc, err := mautrix.NewClient(cfg.Homeserver, id.UserID(cfg.UserID), cfg.AccessToken)
if err != nil {
return nil, fmt.Errorf("create matrix client: %w", err)
}
return &Client{mxc: mxc, cfg: cfg, stopCh: make(chan struct{})}, nil
}

// Start joins the given rooms and begins the sync loop, calling handler for
// every text message received. The sync loop reconnects with exponential
// back-off on errors.
func (c *Client) Start(ctx context.Context, rooms []string, handler MessageHandler) error {
slog.Warn("Matrix E2EE is not enabled; messages are in plaintext")

syncer := c.mxc.Syncer.(*mautrix.DefaultSyncer)
syncer.OnEventType(event.EventMessage, func(ctx2 context.Context, evt *event.Event) {
// Ignore our own messages.
if evt.Sender == id.UserID(c.cfg.UserID) {
return
}
handler(ctx, evt)
})

for _, room := range rooms {
if err := c.join(id.RoomID(room)); err != nil {
slog.Warn("could not join room", "room", room, "err", err)
}
}

go func() {
const backoffMax = 5 * time.Minute
backoff := 2 * time.Second
for {
if err := c.mxc.Sync(); err != nil {
select {
case <-c.stopCh:
return
default:
}
slog.Error("matrix sync error; reconnecting", "err", err, "backoff", backoff)
select {
case <-c.stopCh:
return
case <-time.After(backoff):
}
backoff *= 2
if backoff > backoffMax {
backoff = backoffMax
}
continue
}
select {
case <-c.stopCh:
return
default:
backoff = 2 * time.Second
}
}
}()
return nil
}

// Stop halts the sync loop.
func (c *Client) Stop() {
close(c.stopCh)
c.mxc.StopSync()
}

// SendText sends a plain-text m.text message to the given room.
func (c *Client) SendText(roomID, text string) error {
_, err := c.mxc.SendText(context.Background(), id.RoomID(roomID), text)
return err
}

// SendFormattedMessage sends a message with both a plain-text fallback and
// an HTML-formatted body.
func (c *Client) SendFormattedMessage(roomID, htmlBody, plainBody string) error {
content := event.MessageEventContent{
MsgType:       event.MsgText,
Body:          plainBody,
Format:        event.FormatHTML,
FormattedBody: htmlBody,
}
_, err := c.mxc.SendMessageEvent(
context.Background(),
id.RoomID(roomID),
event.EventMessage,
content,
)
return err
}

// SendReply sends a reply referencing the given event.
func (c *Client) SendReply(roomID, replyToEventID, text string) error {
content := event.MessageEventContent{
MsgType: event.MsgText,
Body:    text,
RelatesTo: &event.RelatesTo{
InReplyTo: &event.InReplyTo{EventID: id.EventID(replyToEventID)},
},
}
_, err := c.mxc.SendMessageEvent(
context.Background(),
id.RoomID(roomID),
event.EventMessage,
content,
)
return err
}

// join joins a room, ignoring "already joined" errors.
func (c *Client) join(roomID id.RoomID) error {
_, err := c.mxc.JoinRoomByID(context.Background(), roomID)
if err != nil {
// mautrix returns an error even when already a member
slog.Info("join room result", "room", roomID, "err", err)
}
return nil
}

// UserID returns the agent's Matrix user ID.
func (c *Client) UserID() string { return c.cfg.UserID }
