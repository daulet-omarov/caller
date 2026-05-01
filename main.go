package main

import (
	"database/sql"
	"log"
	"os"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "github.com/lib/pq"
)

func initDB(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS chat_users (
			chat_id    BIGINT NOT NULL,
			user_id    BIGINT NOT NULL,
			first_name TEXT   NOT NULL DEFAULT '',
			username   TEXT   NOT NULL DEFAULT '',
			PRIMARY KEY (chat_id, user_id)
		)
	`)
	return err
}

func saveUser(db *sql.DB, chatID, userID int64, username, firstName string) {
	_, err := db.Exec(`
		INSERT INTO chat_users (chat_id, user_id, first_name, username)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (chat_id, user_id) DO UPDATE
			SET first_name = EXCLUDED.first_name,
			    username   = EXCLUDED.username
	`, chatID, userID, firstName, username)
	if err != nil {
		log.Printf("saveUser error: %v", err)
	}
}

type userRecord struct {
	userID    int64
	firstName string
}

func getUsers(db *sql.DB, chatID int64) ([]userRecord, error) {
	rows, err := db.Query(`SELECT user_id, first_name FROM chat_users WHERE chat_id = $1`, chatID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []userRecord
	for rows.Next() {
		var u userRecord
		if err := rows.Scan(&u.userID, &u.firstName); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, nil
}

// utf16Len returns the length of s in UTF-16 code units (Telegram uses UTF-16 offsets).
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func buildAllMessage(chatID int64, users []userRecord) tgbotapi.MessageConfig {
	header := "📢 Жігіттер!"
	headerLen := utf16Len(header)

	// One zero-width space per user — invisible but carries the mention entity
	invisible := ""
	for range users {
		invisible += "​"
	}

	text := header + invisible

	entities := make([]tgbotapi.MessageEntity, len(users))
	for i, u := range users {
		entities[i] = tgbotapi.MessageEntity{
			Type:   "text_mention",
			Offset: headerLen + i,
			Length: 1,
			User: &tgbotapi.User{
				ID:        u.userID,
				FirstName: u.firstName,
			},
		}
	}

	msg := tgbotapi.NewMessage(chatID, text)
	msg.Entities = entities
	return msg
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
			users, err := getUsers(db, chatID)
			var reply tgbotapi.MessageConfig

			if err != nil || len(users) == 0 {
				reply = tgbotapi.NewMessage(chatID, "Нет известных участников. Участники добавляются автоматически когда пишут в чат.")
			} else {
				reply = buildAllMessage(chatID, users)
			}

			reply.ReplyToMessageID = msg.MessageID
			if _, err := bot.Send(reply); err != nil {
				log.Printf("send error: %v", err)
			}

		case "start", "help":
			help := "Команды:\n/all — тегнуть всех участников чата\n\nБот запоминает участников, которые писали в чат или вступили в него."
			reply := tgbotapi.NewMessage(chatID, help)
			if _, err := bot.Send(reply); err != nil {
				log.Printf("send error: %v", err)
			}
		}
	}
}
