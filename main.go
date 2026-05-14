package main

import (
	"database/sql"
	"fmt"
	"html"
	"log"
	"os"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "github.com/lib/pq"
)

const ownerID = 821788740

func initDB(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS chat_users (
			chat_id    BIGINT NOT NULL,
			user_id    BIGINT NOT NULL,
			first_name TEXT   NOT NULL DEFAULT '',
			username   TEXT   NOT NULL DEFAULT '',
			PRIMARY KEY (chat_id, user_id)
		);
		CREATE TABLE IF NOT EXISTS blocked_users (
			user_id BIGINT PRIMARY KEY
		);
	`)
	return err
}

func isBlocked(db *sql.DB, userID int64) bool {
	var id int64
	err := db.QueryRow(`SELECT user_id FROM blocked_users WHERE user_id = $1`, userID).Scan(&id)
	return err == nil
}

func blockUser(db *sql.DB, userID int64) error {
	_, err := db.Exec(`INSERT INTO blocked_users (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, userID)
	return err
}

func unblockUser(db *sql.DB, userID int64) error {
	_, err := db.Exec(`DELETE FROM blocked_users WHERE user_id = $1`, userID)
	return err
}

func getBlockedUsers(db *sql.DB) ([]int64, error) {
	rows, err := db.Query(`SELECT user_id FROM blocked_users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
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

func buildAllMessage(chatID int64, users []userRecord, header string) tgbotapi.MessageConfig {
	var sb strings.Builder
	sb.WriteString(html.EscapeString(header))
	for _, u := range users {
		sb.WriteString(fmt.Sprintf(`<a href="tg://user?id=%d">🥵</a>`, u.userID))
	}

	log.Printf("chat_id=%d tagged %d users:", chatID, len(users))
	for _, u := range users {
		log.Printf("  user_id=%d first_name=%q", u.userID, u.firstName)
	}

	msg := tgbotapi.NewMessage(chatID, sb.String())
	msg.ParseMode = "HTML"
	return msg
}

func handleOwnerCommand(bot *tgbotapi.BotAPI, db *sql.DB, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	switch msg.Command() {
	case "block":
		arg := strings.TrimSpace(msg.CommandArguments())
		userID, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "Қолданысы: /block USER_ID"))
			return
		}
		if err := blockUser(db, userID); err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Қате: %v", err)))
			return
		}
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("🚫 %d пайдаланушысы бұғатталды", userID)))

	case "unblock":
		arg := strings.TrimSpace(msg.CommandArguments())
		userID, err := strconv.ParseInt(arg, 10, 64)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "Қолданысы: /unblock USER_ID"))
			return
		}
		if err := unblockUser(db, userID); err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Қате: %v", err)))
			return
		}
		bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ %d пайдаланушысының бұғаты ашылды", userID)))

	case "blocked":
		ids, err := getBlockedUsers(db)
		if err != nil || len(ids) == 0 {
			bot.Send(tgbotapi.NewMessage(chatID, "Бұғатталған пайдаланушылар жоқ."))
			return
		}
		var sb strings.Builder
		sb.WriteString("🚫 Бұғатталғандар:\n\n")
		for _, id := range ids {
			sb.WriteString(fmt.Sprintf("• %d\n", id))
		}
		bot.Send(tgbotapi.NewMessage(chatID, sb.String()))

	case "users":
		rows, err := db.Query(`
			SELECT DISTINCT ON (user_id) user_id, username, first_name
			FROM chat_users
			ORDER BY user_id, first_name
		`)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Қате: %v", err)))
			return
		}
		defer rows.Close()

		var sb strings.Builder
		sb.WriteString("👥 Пайдаланушылар:\n\n")
		count := 0
		for rows.Next() {
			var userID int64
			var username, firstName string
			if err := rows.Scan(&userID, &username, &firstName); err != nil {
				continue
			}
			sb.WriteString(fmt.Sprintf("• %d | @%s | %s\n", userID, username, firstName))
			count++
		}
		if count == 0 {
			bot.Send(tgbotapi.NewMessage(chatID, "Пайдаланушылар жоқ."))
			return
		}
		bot.Send(tgbotapi.NewMessage(chatID, sb.String()))

	case "start", "help":
		help := "Иесінің командалары:\n\n" +
			"/block USER_ID — /all қолжетімділігін жабу\n" +
			"/unblock USER_ID — қолжетімділікті ашу\n" +
			"/blocked — бұғатталғандар тізімі\n" +
			"/users — барлық пайдаланушылар тізімі"
		bot.Send(tgbotapi.NewMessage(chatID, help))
	}
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

	// Register visible commands for users
	bot.Send(tgbotapi.NewSetMyCommands(
		tgbotapi.BotCommand{Command: "all", Description: "Чаттың барлық қатысушыларын белгілеу (калл/call)"},
		tgbotapi.BotCommand{Command: "help", Description: "Анықтама"},
	))

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		msg := update.Message
		chatID := msg.Chat.ID

		// Owner commands in private chat
		if msg.From != nil && msg.From.ID == ownerID && msg.Chat.Type == "private" {
			if msg.IsCommand() {
				handleOwnerCommand(bot, db, msg)
			}
			continue
		}

		if msg.From != nil {
			saveUser(db, chatID, msg.From.ID, msg.From.UserName, msg.From.FirstName)
		}

		if msg.NewChatMembers != nil {
			for _, member := range msg.NewChatMembers {
				saveUser(db, chatID, member.ID, member.UserName, member.FirstName)
			}
		}

		lower := strings.ToLower(msg.Text)
		if lower == "калл" || strings.HasPrefix(lower, "калл ") || lower == "call" || strings.HasPrefix(lower, "call ") {
			if msg.From == nil {
				continue
			}

			if isBlocked(db, msg.From.ID) {
				reply := tgbotapi.NewMessage(chatID, "❌ Сенде бұл командаға қолжетімділік жоқ.")
				reply.ReplyToMessageID = msg.MessageID
				bot.Send(reply)
				continue
			}

			users, err := getUsers(db, chatID)
			if err == nil && len(users) > 0 {
				// Strip the trigger word, use the rest as header
				var header string
				switch {
				case strings.HasPrefix(lower, "калл"):
					header = strings.TrimSpace(msg.Text[len("калл"):])
				case strings.HasPrefix(lower, "call"):
					header = strings.TrimSpace(msg.Text[len("call"):])
				}
				if header == "" {
					header = "📢"
				}
				bot.Send(buildAllMessage(chatID, users, header))
			}

			continue
		}

		if !msg.IsCommand() {
			continue
		}

		switch msg.Command() {
		case "all":
			if msg.From == nil {
				continue
			}

			if isBlocked(db, msg.From.ID) {
				reply := tgbotapi.NewMessage(chatID, "❌ Досым, сенде бұл командаға қолжетімділік жоқ.")
				reply.ReplyToMessageID = msg.MessageID
				bot.Send(reply)
				continue
			}

			users, err := getUsers(db, chatID)
			var reply tgbotapi.MessageConfig

			if err != nil || len(users) == 0 {
				reply = tgbotapi.NewMessage(chatID, "Белгілі қатысушылар жоқ.")
			} else {
				reply = buildAllMessage(chatID, users, "📢 Жігіттер!")
			}

			reply.ReplyToMessageID = msg.MessageID
			if _, err := bot.Send(reply); err != nil {
				log.Printf("send error: %v", err)
			}

		case "start", "help":
			help := "Командалар:\n/all — чаттың барлық қатысушыларын белгілеу (калл/call)"
			reply := tgbotapi.NewMessage(chatID, help)
			if _, err := bot.Send(reply); err != nil {
				log.Printf("send error: %v", err)
			}
		}
	}
}
