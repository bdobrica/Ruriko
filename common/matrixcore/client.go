// Package matrixcore provides shared low-level Matrix client transport
// primitives reused by both Ruriko and Gitai.
package matrixcore

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Config defines connection parameters for a Matrix client.
type Config struct {
	Homeserver  string
	UserID      string
	AccessToken string
}

// Client wraps mautrix.Client with shared lifecycle and send helpers.
type Client struct {
	client *mautrix.Client
}

// New creates a Matrix client.
func New(cfg Config) (*Client, error) {
	client, err := mautrix.NewClient(cfg.Homeserver, id.UserID(cfg.UserID), cfg.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("create matrix client: %w", err)
	}
	return &Client{client: client}, nil
}

// Raw exposes the underlying mautrix client for advanced app-specific wiring.
func (c *Client) Raw() *mautrix.Client {
	return c.client
}

// OnEventType registers an event callback.
func (c *Client) OnEventType(evtType event.Type, handler func(ctx context.Context, evt *event.Event)) {
	syncer := c.client.Syncer.(*mautrix.DefaultSyncer)
	syncer.OnEventType(evtType, handler)
}

// StartSyncLoop starts the /sync reconnect loop in the background.
func (c *Client) StartSyncLoop(stopCh <-chan struct{}) {
	go func() {
		const (
			backoffMin = 2 * time.Second
			backoffMax = 5 * time.Minute
		)
		backoff := backoffMin
		for {
			if err := c.client.Sync(); err != nil {
				select {
				case <-stopCh:
					return
				default:
				}
				slog.Error("matrix sync error; reconnecting", "err", err, "backoff", backoff)
				select {
				case <-stopCh:
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
			case <-stopCh:
				return
			default:
				backoff = backoffMin
			}
		}
	}()
}

// StopSync stops the Matrix sync loop.
func (c *Client) StopSync() {
	c.client.StopSync()
}

// JoinRoomByID joins the given room.
func (c *Client) JoinRoomByID(ctx context.Context, roomID id.RoomID) error {
	_, err := c.client.JoinRoomByID(ctx, roomID)
	return err
}

// SendText sends a plain-text message.
func (c *Client) SendText(ctx context.Context, roomID id.RoomID, text string) error {
	_, err := c.client.SendText(ctx, roomID, text)
	return err
}

// SendMessageEvent sends a generic Matrix event.
func (c *Client) SendMessageEvent(ctx context.Context, roomID id.RoomID, evtType event.Type, content interface{}) error {
	_, err := c.client.SendMessageEvent(ctx, roomID, evtType, content)
	return err
}

// UserTyping updates typing status.
func (c *Client) UserTyping(ctx context.Context, roomID id.RoomID, typing bool, timeout time.Duration) error {
	_, err := c.client.UserTyping(ctx, roomID, typing, timeout)
	return err
}

// GetProfile returns profile details for a user.
func (c *Client) GetProfile(ctx context.Context, userID id.UserID) (*mautrix.RespUserProfile, error) {
	return c.client.GetProfile(ctx, userID)
}
