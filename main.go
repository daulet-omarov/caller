package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "github.com/lib/pq"
)

func initDB(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS chat_users (
			chat_id BIGINT NOT NULL,
			user_id BIGINT NOT NULL,
			mention TEXT NOT NULL,
			PRIMARY KEY (chat_id, user_id)
		)
	`)
	return err
}

func saveUser(db *sql.DB, chatID, userID int64, username, firstName string) {
	var mention string
	if username != "" {
		mention = "@" + username
	} else {
		mention = fmt.Sprintf(`<a href="tg://user?id=%d">%s</a>`, userID, firstName)
	}
	_, err := db.Exec(`
		INSERT INTO chat_users (chat_id, user_id, mention)
		VALUES ($1, $2, $3)
		ON CONFLICT (chat_id, user_id) DO UPDATE SET mention = EXCLUDED.mention
	`, chatID, userID, mention)
	if err != nil {
		log.Printf("saveUser error: %v", err)
	}
}

func getMentions(db *sql.DB, chatID int64) ([]string, error) {
	rows, err := db.Query(`SELECT mention FROM chat_users WHERE chat_id = $1`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var mentions []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		mentions = append(mentions, m)
	}
	return mentions, nil
}

func main() {
	token := os.Getenv("TELEGRAM_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_TOKEN environment variable is required")
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := initDB(db); err != nil {
		log.Fatal("initDB:", err)
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Authorized as @%s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		msg := update.Message
		chatID := msg.Chat.ID

		if msg.From != nil {
			saveUser(db, chatID, msg.From.ID, msg.From.UserName, msg.From.FirstName)
		}

		if msg.NewChatMembers != nil {
			for _, member := range msg.NewChatMembers {
				saveUser(db, chatID, member.ID, member.UserName, member.FirstName)
			}
		}

		if !msg.IsCommand() {
			continue
		}

		switch msg.Command() {
		case "all":
			mentions, err := getMentions(db, chatID)
			var reply tgbotapi.MessageConfig

			if err != nil || len(mentions) == 0 {
				reply = tgbotapi.NewMessage(chatID, "Нет известных участников. Участники добавляются автоматически когда пишут в чат.")
			} else {
				// Hidden mentions wrapped in invisible tag — triggers notifications without showing names
				hidden := `<span class="tg-spoiler">` + strings.Join(mentions, " ") + `</span>`
				text := "📢 Жігіттер!\n" + hidden
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
