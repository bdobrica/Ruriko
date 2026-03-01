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

	"github.com/bdobrica/Ruriko/common/matrixcore"
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
	core   *matrixcore.Client
	cfg    *Config
	stopCh chan struct{}
}

// New creates a Matrix client for the agent but does not start syncing yet.
func New(cfg *Config) (*Client, error) {
	core, err := matrixcore.New(matrixcore.Config{
		Homeserver:  cfg.Homeserver,
		UserID:      cfg.UserID,
		AccessToken: cfg.AccessToken,
	})
	if err != nil {
		return nil, fmt.Errorf("create matrix client: %w", err)
	}
	return &Client{core: core, cfg: cfg, stopCh: make(chan struct{})}, nil
}

// Start joins the given rooms and begins the sync loop, calling handler for
// every text message received. The sync loop reconnects with exponential
// back-off on errors.
func (c *Client) Start(ctx context.Context, rooms []string, handler MessageHandler) error {
	slog.Warn("Matrix E2EE is not enabled; messages are in plaintext")

	c.core.OnEventType(event.EventMessage, func(ctx2 context.Context, evt *event.Event) {
		// Ignore our own messages.
		if evt.Sender == id.UserID(c.cfg.UserID) {
			return
		}
		handler(ctx, evt)
	})

	c.EnsureJoinedRooms(rooms)
	c.core.StartSyncLoop(c.stopCh)
	return nil
}

// EnsureJoinedRooms attempts to join each room in rooms.
//
// It is safe to call at startup and after dynamic config updates. Failures are
// logged and ignored so one problematic room does not block other joins.
func (c *Client) EnsureJoinedRooms(rooms []string) {
	for _, room := range rooms {
		if err := c.join(id.RoomID(room)); err != nil {
			slog.Warn("could not join room", "room", room, "err", err)
		}
	}
}

// Stop halts the sync loop.
func (c *Client) Stop() {
	close(c.stopCh)
	c.core.StopSync()
}

// SendText sends a plain-text m.text message to the given room.
func (c *Client) SendText(roomID, text string) error {
	return c.core.SendText(context.Background(), id.RoomID(roomID), text)
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
	return c.core.SendMessageEvent(context.Background(), id.RoomID(roomID), event.EventMessage, content)
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
	return c.core.SendMessageEvent(context.Background(), id.RoomID(roomID), event.EventMessage, content)
}

// join joins a room, ignoring "already joined" errors.
func (c *Client) join(roomID id.RoomID) error {
	err := c.core.JoinRoomByID(context.Background(), roomID)
	if err != nil {
		// mautrix returns an error even when already a member
		slog.Info("join room result", "room", roomID, "err", err)
	}
	return nil
}

// UserID returns the agent's Matrix user ID.
func (c *Client) UserID() string { return c.cfg.UserID }
