package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// chatUsers stores known users per chat (chatID -> map[userID]username)
type chatUsers struct {
	mu   sync.RWMutex
	data map[int64]map[int64]string
}

func newChatUsers() *chatUsers {
	return &chatUsers{data: make(map[int64]map[int64]string)}
}

func (c *chatUsers) add(chatID, userID int64, username, firstName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.data[chatID]; !ok {
		c.data[chatID] = make(map[int64]string)
	}
	// Prefer @username for mention, fall back to first name with inline mention
	if username != "" {
		c.data[chatID][userID] = "@" + username
	} else {
		// HTML inline mention for users without @username
		c.data[chatID][userID] = fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, userID, firstName)
	}
}

func (c *chatUsers) mentions(chatID int64) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	users, ok := c.data[chatID]
	if !ok {
		return nil
	}
	result := make([]string, 0, len(users))
	for _, mention := range users {
		result = append(result, mention)
	}
	return result
}

func main() {
	token := os.Getenv("TELEGRAM_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_TOKEN environment variable is required")
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Authorized as @%s", bot.Self.UserName)

	known := newChatUsers()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		msg := update.Message
		chatID := msg.Chat.ID

		// Track every user who sends a message
		if msg.From != nil {
			known.add(chatID, msg.From.ID, msg.From.UserName, msg.From.FirstName)
		}

		// Track new members who join the chat
		if msg.NewChatMembers != nil {
			for _, member := range msg.NewChatMembers {
				known.add(chatID, member.ID, member.UserName, member.FirstName)
			}
		}

		if !msg.IsCommand() {
			continue
		}

		switch msg.Command() {
		case "all":
			mentions := known.mentions(chatID)
			var reply tgbotapi.MessageConfig

			if len(mentions) == 0 {
				reply = tgbotapi.NewMessage(chatID, "Нет известных участников. Участники добавляются автоматически когда пишут в чат.")
			} else {
				text := strings.Join(mentions, " ")
				reply = tgbotapi.NewMessage(chatID, text)
				reply.ParseMode = tgbotapi.ModeHTML
			}

			reply.ReplyToMessageID = msg.MessageID
			if _, err := bot.Send(reply); err != nil {
				log.Printf("send error: %v", err)
			}

		case "start", "help":
			help := "Команды:\n/all — тегнуть всех участников чата\n\n" +
				"Бот запоминает участников, которые писали в чат или вступили в него."
			reply := tgbotapi.NewMessage(chatID, help)
			if _, err := bot.Send(reply); err != nil {
				log.Printf("send error: %v", err)
			}
		}
	}
}
