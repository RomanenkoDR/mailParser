package main

import (
	"fmt"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
	"io/ioutil"
	"log"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	IMAP_SERVER = "imap.mail.ru"
	FILES_DIR   = "files"
	FILE_TYPES  = map[string]string{
		".bin":    "bins",
		".pdf":    "pdf",
		".xlsx":   "excel",
		".xls":    "excel",
		".doc":    "docs",
		".docx":   "docs",
		".jpg":    "jpg",
		".dbf":    "dbf",
		".xml":    "xml",
		"another": "another",
	}
	LIST_CREADS = [][]string{
		{"02mok.tch@bpo.travel", "zx9wqziqHqhPBCzaiAD3"},
		{"05msv.tch@bpo.travel", "HNkfdiwP685bYPwwxd2T"},
		{"52msa.tch@bpo.travel", "DDUvrMhmr1QCGxDu2MuY"},
		{"66msa.tch@bpo.travel", "AfsNujHPcrca0hV8pepY"},
	}
)

type MessageAttachment struct {
	Name    string
	Content []byte
}

func main() {
	checkDirs()
	for {
		for _, cred := range LIST_CREADS {
			// Выводим текущую почту, которую сканируем
			log.Printf("Сканирование почтового ящика: %s", cred[0])

			session := startSession(cred[0], cred[1], IMAP_SERVER)
			if session == nil {
				log.Println("Не удалось установить сессию.")
				continue
			}

			totalMails, unreadMailIDs := getMails(session)
			log.Printf("Всего писем для %s: %d, Непрочитанные письма: %d", cred[0], totalMails, len(unreadMailIDs))

			for _, id := range unreadMailIDs {
				msgData := getMail(session, id)
				msg := parseMessage(msgData)

				attachments := findAttachments(msg)
				if len(attachments) > 0 {
					log.Printf("Найдено вложений: %d в письме с ID %d для %s", len(attachments), id, cred[0])
				} else {
					log.Printf("В письме с ID %d вложений не найдено для %s", id, cred[0])
				}

				for _, attachment := range attachments {
					fileType := getFileType(attachment.Name)
					saveDir := getSubDirNameByFileType(cred[0], fileType) // Путь с логином
					downloadFile(attachment, saveDir)
				}

				markAsRead(session, id)
			}

			stopSession(session)
		}
		time.Sleep(30 * time.Minute) // Задержка в 30 минут
	}
}

func startSession(username, password, imapServer string) *client.Client {
	c, err := client.DialTLS(imapServer+":993", nil)
	if err != nil {
		log.Fatal(err)
	}
	if err := c.Login(username, password); err != nil {
		log.Fatal(err)
	}
	return c
}

func stopSession(c *client.Client) {
	if err := c.Logout(); err != nil {
		log.Printf("Ошибка при выходе из сессии: %v", err)
	}
}

func getMails(c *client.Client) (int, []uint32) {
	// Получаем общее количество писем
	mailbox, err := c.Select("INBOX", false)
	if err != nil {
		log.Fatal(err)
	}
	totalMails := int(mailbox.Messages) // Общее количество писем

	// Поиск непрочитанных писем
	searchCriteria := imap.NewSearchCriteria()
	searchCriteria.WithoutFlags = []string{imap.SeenFlag}

	unreadMailIDs, err := c.Search(searchCriteria)
	if err != nil {
		log.Fatal(err)
	}

	return totalMails, unreadMailIDs
}

func getMail(c *client.Client, id uint32) *imap.Message {
	seqset := new(imap.SeqSet)
	seqset.AddNum(id)

	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.Fetch(seqset, []imap.FetchItem{imap.FetchEnvelope, imap.FetchBodyStructure, imap.FetchRFC822}, messages)
	}()

	msg := <-messages
	if msg == nil {
		log.Fatal("Server didn't return message")
	}

	if err := <-done; err != nil {
		log.Fatal(err)
	}

	return msg
}

func parseMessage(msg *imap.Message) *mail.Message {
	section := &imap.BodySectionName{}
	r := msg.GetBody(section)
	if r == nil {
		log.Fatal("Не удалось получить тело сообщения")
	}

	// Прочитать содержимое сообщения
	rawMsg, err := ioutil.ReadAll(r)
	if err != nil {
		log.Fatalf("Ошибка при чтении сообщения: %v", err)
	}

	var decodedMsg []byte

	// Проверяем и обрабатываем кодировки
	if strings.Contains(string(rawMsg), "windows-1251") {
		log.Println("Обнаружена кодировка windows-1251, выполняется декодирование")
		reader := transform.NewReader(strings.NewReader(string(rawMsg)), charmap.Windows1251.NewDecoder())
		decodedMsg, err = ioutil.ReadAll(reader)
		if err != nil {
			log.Fatalf("Ошибка декодирования windows-1251: %v", err)
		}
	} else if strings.Contains(string(rawMsg), "koi8-r") {
		log.Println("Обнаружена кодировка koi8-r, выполняется декодирование")
		reader := transform.NewReader(strings.NewReader(string(rawMsg)), charmap.KOI8R.NewDecoder())
		decodedMsg, err = ioutil.ReadAll(reader)
		if err != nil {
			log.Fatalf("Ошибка декодирования koi8-r: %v", err)
		}
	} else {
		log.Println("Неизвестная кодировка, используется по умолчанию utf-8")
		decodedMsg = rawMsg // Оставляем исходное сообщение
	}

	// Создаем mail.Message из декодированного текста
	msgReader := strings.NewReader(string(decodedMsg))
	parsedMsg, err := mail.ReadMessage(msgReader)
	if err != nil {
		log.Fatalf("Ошибка при парсинге сообщения: %v", err)
	}

	return parsedMsg
}

func findAttachments(msg *mail.Message) []MessageAttachment {
	var attachments []MessageAttachment

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		log.Fatalf("Ошибка при разборе Content-Type: %v", err)
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		mr := multipart.NewReader(msg.Body, params["boundary"])

		for {
			part, err := mr.NextPart()
			if err != nil {
				break
			}

			if part.FileName() != "" {
				// Это вложение, сохраняем его
				content, err := ioutil.ReadAll(part)
				if err != nil {
					log.Fatalf("Ошибка при чтении вложения: %v", err)
				}

				attachment := MessageAttachment{
					Name:    part.FileName(),
					Content: content,
				}
				attachments = append(attachments, attachment)
			}
		}
	}

	return attachments
}

func saveAttachment(part *multipart.Part) {
	// Читаем содержимое файла
	content, err := ioutil.ReadAll(part)
	if err != nil {
		log.Fatalf("Ошибка при чтении вложения: %v", err)
	}

	// Сохраняем файл
	err = ioutil.WriteFile(part.FileName(), content, 0644)
	if err != nil {
		log.Fatalf("Ошибка при сохранении файла: %v", err)
	}

	log.Printf("Вложение %s успешно сохранено", part.FileName())
}

func getFileType(filename string) string {
	return filepath.Ext(filename)
}

func getSubDirNameByFileType(email string, fileType string) string {
	subDir := FILE_TYPES[fileType]
	if subDir == "" {
		subDir = "another"
	}

	// Путь к директории с логином почты
	dir := filepath.Join(FILES_DIR, strings.TrimSuffix(email, "@bpo.travel"), subDir)

	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		log.Fatal(err)
	}

	return dir
}

func downloadFile(attachment MessageAttachment, dir string) {
	filepath := filepath.Join(dir, attachment.Name)
	if err := os.WriteFile(filepath, attachment.Content, os.ModePerm); err != nil {
		log.Fatal(err)
	}
	fmt.Println("Файл записан по пути:", filepath)
}

func checkDirs() {
	for _, cred := range LIST_CREADS {
		dir := getDirsName(cred[0])
		createSubDir(dir)
	}
	fmt.Println("Дирректории инициализированы!")
}

func getDirsName(mail string) string {
	return strings.TrimSuffix(mail, "@bpo.travel")
}

func createSubDir(name string) {
	dir := filepath.Join(FILES_DIR, name)
	if err := os.MkdirAll(dir, os.ModePerm); err != nil {
		log.Fatal(err)
	}
}

func markAsRead(c *client.Client, id uint32) {
	seqset := new(imap.SeqSet)
	seqset.AddNum(id)

	item := imap.FormatFlagsOp(imap.AddFlags, true)
	flags := []interface{}{imap.SeenFlag}
	if err := c.Store(seqset, item, flags, nil); err != nil {
		log.Printf("Ошибка при пометке письма %d как прочитанного: %v", id, err)
	} else {
		log.Printf("Письмо %d успешно помечено как прочитанное", id)
	}
}
