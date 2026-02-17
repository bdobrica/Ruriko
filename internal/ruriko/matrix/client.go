// Package matrix provides Matrix client functionality for Ruriko
package matrix

import (
	"context"
	"fmt"
	"strings"
	"time"

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
}

// Client wraps the Matrix client
type Client struct {
	client     *mautrix.Client
	config     *Config
	stopCh     chan struct{}
	msgHandler MessageHandler
}

// MessageHandler processes incoming Matrix messages
type MessageHandler func(ctx context.Context, evt *event.Event)

// New creates a new Matrix client
func New(config *Config) (*Client, error) {
	client, err := mautrix.NewClient(config.Homeserver, id.UserID(config.UserID), config.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create Matrix client: %w", err)
	}

	return &Client{
		client: client,
		config: config,
		stopCh: make(chan struct{}),
	}, nil
}

// Start begins syncing with the Matrix homeserver
func (c *Client) Start(ctx context.Context, handler MessageHandler) error {
	c.msgHandler = handler

	// Set up event handler
	syncer := c.client.Syncer.(*mautrix.DefaultSyncer)
	syncer.OnEventType(event.EventMessage, c.handleMessage)

	// Join admin rooms
	for _, roomID := range c.config.AdminRooms {
		if err := c.joinRoom(id.RoomID(roomID)); err != nil {
			return fmt.Errorf("failed to join admin room %s: %w", roomID, err)
		}
	}

	// Start syncing in background
	go func() {
		if err := c.client.Sync(); err != nil {
			fmt.Printf("Matrix sync error: %v\n", err)
		}
	}()

	return nil
}

// Stop stops the Matrix client
func (c *Client) Stop() {
	close(c.stopCh)
	c.client.StopSync()
}

// SendMessage sends a text message to a room
func (c *Client) SendMessage(roomID, message string) error {
	_, err := c.client.SendText(context.Background(), id.RoomID(roomID), message)
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

	_, err := c.client.SendMessageEvent(context.Background(), id.RoomID(roomID), event.EventMessage, &content)
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

	_, err := c.client.SendMessageEvent(context.Background(), id.RoomID(roomID), event.EventMessage, &content)
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

	_, err := c.client.SendMessageEvent(context.Background(), id.RoomID(roomID), event.EventMessage, &content)
	if err != nil {
		return fmt.Errorf("failed to send notice: %w", err)
	}
	return nil
}

// SetTyping sets typing indicator
func (c *Client) SetTyping(roomID string, typing bool, timeout time.Duration) error {
	_, err := c.client.UserTyping(context.Background(), id.RoomID(roomID), typing, timeout)
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
	_, err := c.client.JoinRoomByID(context.Background(), roomID)
	if err != nil {
		// Check if already joined
		if strings.Contains(err.Error(), "already in room") {
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
	profile, err := c.client.GetProfile(context.Background(), id.UserID(userID))
	if err != nil {
		return "", fmt.Errorf("failed to get profile: %w", err)
	}
	return profile.DisplayName, nil
}
