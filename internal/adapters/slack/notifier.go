package slack

import (
	"context"
	"fmt"

	"github.com/qf-studio/pilot/internal/adapters"
)

// Config holds Slack adapter configuration
type Config struct {
	Enabled         bool            `yaml:"enabled"`
	BotToken        string          `yaml:"bot_token"`
	AppToken        string          `yaml:"app_token"`
	Channel         string          `yaml:"channel"`
	SigningSecret   string          `yaml:"signing_secret"`
	SocketMode      bool            `yaml:"socket_mode"`
	AllowedUsers    []string        `yaml:"allowed_users"`
	AllowedChannels []string        `yaml:"allowed_channels"`
	Approval        *ApprovalConfig `yaml:"approval,omitempty"`
}

// DefaultConfig returns default Slack configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled:         false,
		Channel:         "#dev-notifications",
		SocketMode:      false,
		AllowedUsers:    []string{},
		AllowedChannels: []string{},
		Approval:        DefaultApprovalConfig(),
	}
}

// Notifier sends notifications to Slack
type Notifier struct {
	client  *Client
	channel string
}

// NewNotifier creates a new Slack notifier
func NewNotifier(config *Config) *Notifier {
	return &Notifier{
		client:  NewClient(config.BotToken),
		channel: config.Channel,
	}
}

// TaskStarted notifies that a task has started
func (n *Notifier) TaskStarted(ctx context.Context, taskID, title string) error {
	msg := &Message{
		Channel: n.channel,
		Blocks: []Block{
			{
				Type: "section",
				Text: &TextObject{
					Type: "mrkdwn",
					Text: fmt.Sprintf("🚀 *Pilot started task*\n`%s` %s", taskID, title),
				},
			},
		},
	}

	_, err := n.client.PostMessage(ctx, msg)
	return err
}

// TaskProgress notifies about task progress
func (n *Notifier) TaskProgress(ctx context.Context, taskID, status string, progress int) error {
	progressBar := adapters.GenerateProgressBar(progress, 10)

	msg := &Message{
		Channel: n.channel,
		Blocks: []Block{
			{
				Type: "section",
				Text: &TextObject{
					Type: "mrkdwn",
					Text: fmt.Sprintf("⏳ *Task Progress*\n`%s` %s\n%s %d%%", taskID, status, progressBar, progress),
				},
			},
		},
	}

	_, err := n.client.PostMessage(ctx, msg)
	return err
}

// TaskCompleted notifies that a task has completed
func (n *Notifier) TaskCompleted(ctx context.Context, taskID, title, prURL string) error {
	text := fmt.Sprintf("✅ *Pilot completed task*\n`%s` %s", taskID, title)
	if prURL != "" {
		text += fmt.Sprintf("\n\n<PR ready for review|%s>", prURL)
	}

	msg := &Message{
		Channel: n.channel,
		Blocks: []Block{
			{
				Type: "section",
				Text: &TextObject{
					Type: "mrkdwn",
					Text: text,
				},
			},
		},
		Attachments: []Attachment{
			{
				Color: "good",
			},
		},
	}

	_, err := n.client.PostMessage(ctx, msg)
	return err
}

// TaskFailed notifies that a task has failed
func (n *Notifier) TaskFailed(ctx context.Context, taskID, title, errorMsg string) error {
	msg := &Message{
		Channel: n.channel,
		Blocks: []Block{
			{
				Type: "section",
				Text: &TextObject{
					Type: "mrkdwn",
					Text: fmt.Sprintf("❌ *Pilot task failed*\n`%s` %s\n\n```%s```", taskID, title, errorMsg),
				},
			},
		},
		Attachments: []Attachment{
			{
				Color: "danger",
			},
		},
	}

	_, err := n.client.PostMessage(ctx, msg)
	return err
}

// PRReady notifies that a PR is ready for review
func (n *Notifier) PRReady(ctx context.Context, taskID, title, prURL string, filesChanged int) error {
	msg := &Message{
		Channel: n.channel,
		Blocks: []Block{
			{
				Type: "section",
				Text: &TextObject{
					Type: "mrkdwn",
					Text: fmt.Sprintf("🔔 *PR Ready for Review*\n`%s` %s\n\n<%s|View PR> • %d files changed", taskID, title, prURL, filesChanged),
				},
			},
		},
		Attachments: []Attachment{
			{
				Color: "#6366f1",
			},
		},
	}

	_, err := n.client.PostMessage(ctx, msg)
	return err
}

