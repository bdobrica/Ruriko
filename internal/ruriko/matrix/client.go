// Package matrix provides Matrix client functionality for Ruriko
package matrix

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bdobrica/Ruriko/common/matrixcore"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Config holds Matrix client configuration
type Config struct {
	Homeserver  string
	UserID      string
	AccessToken string
	AdminRooms  []string // Room IDs where Ruriko accepts commands
	// DB is an optional SQLite connection used to persist the Matrix sync
	// token (next_batch) across restarts.  When nil, an in-memory store is
	// used and all room history will be replayed on every restart.
	DB *sql.DB
}

// Client wraps the Matrix client
type Client struct {
	core       *matrixcore.Client
	config     *Config
	stopCh     chan struct{}
	msgHandler MessageHandler
}

// MessageHandler processes incoming Matrix messages
type MessageHandler func(ctx context.Context, evt *event.Event)

// New creates a new Matrix client
func New(config *Config) (*Client, error) {
	core, err := matrixcore.New(matrixcore.Config{
		Homeserver:  config.Homeserver,
		UserID:      config.UserID,
		AccessToken: config.AccessToken,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Matrix client: %w", err)
	}

	c := &Client{
		core:   core,
		config: config,
		stopCh: make(chan struct{}),
	}

	// Attach a persistent sync store so the bot resumes from the last known
	// position after a restart instead of replaying the full room history.
	if config.DB != nil {
		core.Raw().Store = newDBSyncStore(config.DB)
		slog.Info("Matrix sync store: using persistent SQLite store")
	} else {
		slog.Warn("Matrix sync store: no DB configured, using in-memory store (history will replay on restart)")
	}

	return c, nil
}

// Start begins syncing with the Matrix homeserver
func (c *Client) Start(ctx context.Context, handler MessageHandler) error {
	c.msgHandler = handler

	// NOTE: E2EE (end-to-end encryption) is not currently implemented.
	// All messages are sent and received in plaintext. Secret values
	// transmitted via Matrix commands are visible in room history.
	// Implementing E2EE requires olm session management via the mautrix
	// crypto store and additional key lifecycle handling.
	slog.Warn("Matrix E2EE is not enabled; messages are transmitted in plaintext")

	// Set up event handler
	c.core.OnEventType(event.EventMessage, c.handleMessage)

	// Join admin rooms
	for _, roomID := range c.config.AdminRooms {
		if err := c.joinRoom(id.RoomID(roomID)); err != nil {
			return fmt.Errorf("failed to join admin room %s: %w", roomID, err)
		}
	}

	// Start syncing in background with exponential back-off reconnection.
	// Without retries a transient homeserver error would silently kill the
	// sync goroutine and leave the bot deaf to all new messages.
	c.core.StartSyncLoop(c.stopCh)

	return nil
}

// Stop stops the Matrix client
func (c *Client) Stop() {
	close(c.stopCh)
	c.core.StopSync()
}

// SendMessage sends a text message to a room
func (c *Client) SendMessage(roomID, message string) error {
	err := c.core.SendText(context.Background(), id.RoomID(roomID), message)
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	return nil
}

// SendFormattedMessage sends a formatted message (HTML + plain text fallback)
func (c *Client) SendFormattedMessage(roomID, html, plaintext string) error {
	content := event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          plaintext,
		Format:        event.FormatHTML,
		FormattedBody: html,
	}

	err := c.core.SendMessageEvent(context.Background(), id.RoomID(roomID), event.EventMessage, &content)
	if err != nil {
		return fmt.Errorf("failed to send formatted message: %w", err)
	}
	return nil
}

// ReplyToMessage sends a reply to a specific message
func (c *Client) ReplyToMessage(roomID, eventID, message string) error {
	content := event.MessageEventContent{
		MsgType: event.MsgText,
		Body:    message,
		RelatesTo: &event.RelatesTo{
			InReplyTo: &event.InReplyTo{
				EventID: id.EventID(eventID),
			},
		},
	}

	err := c.core.SendMessageEvent(context.Background(), id.RoomID(roomID), event.EventMessage, &content)
	if err != nil {
		return fmt.Errorf("failed to send reply: %w", err)
	}
	return nil
}

// SendNotice sends a notice message (less intrusive than normal messages)
func (c *Client) SendNotice(roomID, message string) error {
	content := event.MessageEventContent{
		MsgType: event.MsgNotice,
		Body:    message,
	}

	err := c.core.SendMessageEvent(context.Background(), id.RoomID(roomID), event.EventMessage, &content)
	if err != nil {
		return fmt.Errorf("failed to send notice: %w", err)
	}
	return nil
}

// SetTyping sets typing indicator
func (c *Client) SetTyping(roomID string, typing bool, timeout time.Duration) error {
	err := c.core.UserTyping(context.Background(), id.RoomID(roomID), typing, timeout)
	if err != nil {
		return fmt.Errorf("failed to set typing: %w", err)
	}
	return nil
}

// IsAdminRoom checks if a room is configured as an admin room
func (c *Client) IsAdminRoom(roomID string) bool {
	for _, adminRoom := range c.config.AdminRooms {
		if adminRoom == roomID {
			return true
		}
	}
	return false
}

// handleMessage processes incoming messages
func (c *Client) handleMessage(ctx context.Context, evt *event.Event) {
	// Ignore our own messages
	if evt.Sender == id.UserID(c.config.UserID) {
		return
	}

	// Only process text messages
	msgContent := evt.Content.AsMessage()
	if msgContent == nil || msgContent.MsgType != event.MsgText {
		return
	}

	// Only process messages in admin rooms
	if !c.IsAdminRoom(evt.RoomID.String()) {
		return
	}

	// Call the registered handler
	if c.msgHandler != nil {
		c.msgHandler(ctx, evt)
	}
}

// joinRoom attempts to join a room
func (c *Client) joinRoom(roomID id.RoomID) error {
	err := c.core.JoinRoomByID(context.Background(), roomID)
	if err != nil {
		// M_FORBIDDEN is returned by homeservers when the bot is already a member
		// of the room. Use mautrix's typed error check instead of string matching.
		if errors.Is(err, mautrix.MForbidden) {
			slog.Warn("joinRoom: already a member or access denied, continuing", "room", roomID)
			return nil
		}
		return err
	}
	return nil
}

// GetUserID returns the client's user ID
func (c *Client) GetUserID() string {
	return c.config.UserID
}

// GetDisplayName gets a user's display name
func (c *Client) GetDisplayName(userID string) (string, error) {
	profile, err := c.core.GetProfile(context.Background(), id.UserID(userID))
	if err != nil {
		return "", fmt.Errorf("failed to get profile: %w", err)
	}
	return profile.DisplayName, nil
}
