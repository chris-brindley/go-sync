// Package conversation synchronises email addresses with Slack conversations.
//
// In order to use this adapter, you'll need an authenticated Slack client and for the Slack app to have been added
// to the conversation.
package conversation

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ovotech/go-sync/internal/types"
	"github.com/slack-go/slack"
)

// iSlackConversation is a subset of the Slack Client, and used to build mocks for easy testing.
type iSlackConversation interface {
	GetUsersInConversation(params *slack.GetUsersInConversationParameters) ([]string, string, error)
	GetUsersInfo(users ...string) (*[]slack.User, error)
	GetUserByEmail(email string) (*slack.User, error)
	InviteUsersToConversation(channelID string, users ...string) (*slack.Channel, error)
	KickUserFromConversation(channelID string, user string) error
}

type Conversation struct {
	client           iSlackConversation
	conversationName string
	cache            map[string]string // This stores the Slack ID -> email mapping for use with the Remove method.
	logger           types.Logger
}

// ErrCacheEmpty shouldn't realistically be raised unless the adapter is being used outside of Go Sync.
var ErrCacheEmpty = errors.New("cache is empty - run Get()")

// OptionLogger can be used to set a custom logger.
func OptionLogger(logger types.Logger) func(*Conversation) {
	return func(conversation *Conversation) {
		conversation.logger = logger
	}
}

// New instantiates a new Slack conversation adapter.
func New(client *slack.Client, channelName string, optsFn ...func(conversation *Conversation)) *Conversation {
	conversation := &Conversation{
		client:           client,
		conversationName: channelName,
		cache:            make(map[string]string),
		logger:           log.New(os.Stderr, "[go-sync/slack/conversation] ", log.LstdFlags|log.Lshortfile|log.Lmsgprefix),
	}

	for _, fn := range optsFn {
		fn(conversation)
	}

	return conversation
}

// getListOfSlackUsernames gets a list of Slack users in a conversation, and paginates through the results.
func (c *Conversation) getListOfSlackUsernames() ([]string, error) {
	var (
		cursor string
		users  []string
		err    error
	)

	for {
		params := &slack.GetUsersInConversationParameters{
			ChannelID: c.conversationName,
			Cursor:    cursor,
			Limit:     50, //nolint:gomnd
		}

		var pageOfUsers []string

		pageOfUsers, cursor, err = c.client.GetUsersInConversation(params)
		if err != nil {
			return nil, fmt.Errorf("getusersinconversation(%s) -> %w", c.conversationName, err)
		}

		users = append(users, pageOfUsers...)

		if cursor == "" {
			break
		}
	}

	return users, nil
}

// Get emails of Slack users in a conversation.
func (c *Conversation) Get(_ context.Context) ([]string, error) {
	c.logger.Printf("Fetching accounts from Slack conversation %s", c.conversationName)

	slackUsers, err := c.getListOfSlackUsernames()
	if err != nil {
		return nil, fmt.Errorf("slack.conversation.get.getlistofslackusernames -> %w", err)
	}

	users, err := c.client.GetUsersInfo(slackUsers...)
	if err != nil {
		return nil, fmt.Errorf("slack.conversation.get.getusersinfo -> %w", err)
	}

	emails := make([]string, 0, len(*users))

	for _, user := range *users {
		if !user.IsBot {
			emails = append(emails, user.Profile.Email)

			// Add the email -> ID map for use with Remove method.
			c.cache[user.Profile.Email] = user.ID
		}
	}

	c.logger.Println("Fetched accounts successfully")

	return emails, nil
}

// Add emails to a Slack conversation.
func (c *Conversation) Add(_ context.Context, emails []string) error {
	c.logger.Printf("Adding %s to Slack conversation %s", emails, c.conversationName)

	slackIds := make([]string, len(emails))

	for index, email := range emails {
		user, err := c.client.GetUserByEmail(email)
		if err != nil {
			return fmt.Errorf("slack.conversation.add.getuserbyemail(%s) -> %w", email, err)
		}

		slackIds[index] = user.ID
		// Add the user to the cache.
		c.cache[email] = user.ID
	}

	_, err := c.client.InviteUsersToConversation(c.conversationName, slackIds...)
	if err != nil {
		c.cache = nil

		return fmt.Errorf("slack.conversation.add.inviteuserstoconversation(%s, ...) -> %w", c.conversationName, err)
	}

	c.logger.Println("Finished adding accounts successfully")

	return nil
}

// Remove emails from a Slack conversation.
func (c *Conversation) Remove(_ context.Context, emails []string) error {
	c.logger.Printf("Removing %s from Slack conversation %s", emails, c.conversationName)

	// If the cache hasn't been generated, regenerate it.
	if len(c.cache) == 0 {
		return fmt.Errorf("slack.conversation.remove -> %w", ErrCacheEmpty)
	}

	for _, email := range emails {
		err := c.client.KickUserFromConversation(c.conversationName, c.cache[email])
		if err != nil {
			return fmt.Errorf(
				"slack.conversation.remove.kickuserfromconversation(%s, %s) -> %w",
				c.conversationName,
				c.cache[email],
				err,
			)
		}

		// Delete the entry from the cache.
		delete(c.cache, email)

		// To prevent rate limiting, sleep for 1 second after each kick.
		time.Sleep(1 * time.Second)
	}

	c.logger.Println("Finished removing accounts successfully")

	return nil
}