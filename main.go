package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	_ "github.com/lib/pq"
)

const (
	dailyLimit = 3
	ownerID    = 821788740
)

func initDB(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS chat_users (
			chat_id    BIGINT NOT NULL,
			user_id    BIGINT NOT NULL,
			first_name TEXT   NOT NULL DEFAULT '',
			username   TEXT   NOT NULL DEFAULT '',
			PRIMARY KEY (chat_id, user_id)
		);
		CREATE TABLE IF NOT EXISTS call_usage (
			chat_id    BIGINT NOT NULL,
			user_id    BIGINT NOT NULL,
			used_date  DATE   NOT NULL DEFAULT CURRENT_DATE,
			count      INT    NOT NULL DEFAULT 0,
			PRIMARY KEY (chat_id, user_id, used_date)
		);
		CREATE TABLE IF NOT EXISTS user_limits (
			user_id     BIGINT PRIMARY KEY,
			daily_limit INT    NOT NULL
		);
	`)
	return err
}

func getUserLimit(db *sql.DB, userID int64) int {
	var limit int
	err := db.QueryRow(`SELECT daily_limit FROM user_limits WHERE user_id = $1`, userID).Scan(&limit)
	if err == sql.ErrNoRows {
		return dailyLimit
	}
	if err != nil {
		log.Printf("getUserLimit error: %v", err)
		return dailyLimit
	}
	return limit
}

func setUserLimit(db *sql.DB, userID int64, limit int) error {
	if limit == 0 {
		_, err := db.Exec(`DELETE FROM user_limits WHERE user_id = $1`, userID)
		return err
	}
	_, err := db.Exec(`
		INSERT INTO user_limits (user_id, daily_limit) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET daily_limit = EXCLUDED.daily_limit
	`, userID, limit)
	return err
}

func getAllLimits(db *sql.DB) (map[int64]int, error) {
	rows, err := db.Query(`SELECT user_id, daily_limit FROM user_limits`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[int64]int)
	for rows.Next() {
		var userID int64
		var limit int
		if err := rows.Scan(&userID, &limit); err != nil {
			return nil, err
		}
		result[userID] = limit
	}
	return result, nil
}

func checkAndIncrement(db *sql.DB, chatID, userID int64) (remaining int, err error) {
	limit := getUserLimit(db, userID)
	if limit < 0 {
		return 999, nil // unlimited
	}

	var count int
	err = db.QueryRow(`
		INSERT INTO call_usage (chat_id, user_id, used_date, count)
		VALUES ($1, $2, CURRENT_DATE, 1)
		ON CONFLICT (chat_id, user_id, used_date) DO UPDATE
			SET count = call_usage.count + 1
		RETURNING count
	`, chatID, userID).Scan(&count)
	if err != nil {
		return 0, err
	}

	if count > limit {
		db.Exec(`
			UPDATE call_usage SET count = count - 1
			WHERE chat_id = $1 AND user_id = $2 AND used_date = CURRENT_DATE
		`, chatID, userID)
		return -1, nil
	}
	return limit - count, nil
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

func buildAllMessage(chatID int64, users []userRecord) tgbotapi.MessageConfig {
	header := "📢 Жігіттер!"
	headerLen := utf16Len(header)

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

func handleOwnerCommand(bot *tgbotapi.BotAPI, db *sql.DB, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID

	switch msg.Command() {
	case "setlimit":
		// /setlimit USER_ID LIMIT
		args := strings.Fields(msg.CommandArguments())
		if len(args) != 2 {
			bot.Send(tgbotapi.NewMessage(chatID, "Использование: /setlimit USER_ID LIMIT\n\nПример: /setlimit 123456789 5\nДля сброса лимита: /setlimit 123456789 0 (вернёт к дефолту)"))
			return
		}

		userID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Неверный USER_ID"))
			return
		}

		limit, err := strconv.Atoi(args[1])
		if err != nil || limit < 0 {
			bot.Send(tgbotapi.NewMessage(chatID, "❌ Неверный лимит"))
			return
		}

		if err := setUserLimit(db, userID, limit); err != nil {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Ошибка: %v", err)))
			return
		}

		if limit == 0 {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Лимит для %d сброшен (дефолт: %d)", userID, dailyLimit)))
		} else {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("✅ Лимит для %d установлен: %d раз/день", userID, limit)))
		}

	case "limits":
		limits, err := getAllLimits(db)
		if err != nil || len(limits) == 0 {
			bot.Send(tgbotapi.NewMessage(chatID, fmt.Sprintf("Кастомных лимитов нет. Дефолт: %d раз/день", dailyLimit)))
			return
		}
		var sb strings.Builder
		sb.WriteString("📋 Кастомные лимиты:\n\n")
		for uid, lim := range limits {
			sb.WriteString(fmt.Sprintf("• %d → %d раз/день\n", uid, lim))
		}
		bot.Send(tgbotapi.NewMessage(chatID, sb.String()))

	case "start", "help":
		help := "Команды владельца:\n\n" +
			"/setlimit USER_ID N — установить лимит N раз/день\n" +
			"/setlimit USER_ID 0 — сбросить к дефолту\n" +
			"/limits — список кастомных лимитов\n\n" +
			fmt.Sprintf("Дефолтный лимит: %d раз/день", dailyLimit)
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

		if !msg.IsCommand() {
			continue
		}

		switch msg.Command() {
		case "all":
			if msg.From == nil {
				continue
			}

			remaining, err := checkAndIncrement(db, chatID, msg.From.ID)
			if err != nil {
				log.Printf("checkAndIncrement error: %v", err)
				continue
			}

			if remaining == -1 {
				limit := getUserLimit(db, msg.From.ID)
				reply := tgbotapi.NewMessage(chatID, fmt.Sprintf("❌ Лимит исчерпан. Можно использовать /all не более %d раз в день.", limit))
				reply.ReplyToMessageID = msg.MessageID
				bot.Send(reply)
				continue
			}

			users, err := getUsers(db, chatID)
			var reply tgbotapi.MessageConfig

			if err != nil || len(users) == 0 {
				reply = tgbotapi.NewMessage(chatID, "Нет известных участников.")
			} else {
				reply = buildAllMessage(chatID, users)
			}

			reply.ReplyToMessageID = msg.MessageID
			if _, err := bot.Send(reply); err != nil {
				log.Printf("send error: %v", err)
			}

		case "start", "help":
			limit := getUserLimit(db, msg.From.ID)
			help := fmt.Sprintf("Команды:\n/all — тегнуть всех участников чата (лимит: %d раз в день)\n\nБот запоминает участников, которые писали в чат или вступили в него.", limit)
			reply := tgbotapi.NewMessage(chatID, help)
			if _, err := bot.Send(reply); err != nil {
				log.Printf("send error: %v", err)
			}
		}
	}
}
