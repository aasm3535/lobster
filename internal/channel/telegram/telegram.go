// Package telegram implements the Channel interface against the Telegram Bot API
// using plain HTTP long-polling — no SDK, no dependencies.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aasm3535/lobster/internal/channel"
	"github.com/aasm3535/lobster/internal/llm"
)

type Bot struct {
	token  string
	client *http.Client
}

func New(token string) *Bot {
	return &Bot{
		token:  token,
		client: &http.Client{Timeout: 70 * time.Second},
	}
}

func (b *Bot) Name() string { return "telegram" }

func (b *Bot) endpoint(method string) string {
	return fmt.Sprintf("https://api.telegram.org/bot%s/%s", b.token, method)
}

func (b *Bot) api(ctx context.Context, method string, payload any) (json.RawMessage, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint(method), bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return b.do(req, method)
}

// do executes a prepared request and unwraps Telegram's {ok, result, description} envelope.
func (b *Bot) do(req *http.Request, method string) (json.RawMessage, error) {
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var env struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("telegram decode: %w", err)
	}
	if !env.OK {
		return nil, fmt.Errorf("telegram %s: %s", method, env.Description)
	}
	return env.Result, nil
}

type update struct {
	UpdateID int `json:"update_id"`
	Message  *struct {
		MessageID int `json:"message_id"`
		From      struct {
			Username  string `json:"username"`
			FirstName string `json:"first_name"`
		} `json:"from"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		Text    string `json:"text"`
		Caption string `json:"caption"`
		Photo   []struct {
			FileID string `json:"file_id"`
		} `json:"photo"`
		Document *struct {
			FileID   string `json:"file_id"`
			MimeType string `json:"mime_type"`
		} `json:"document"`
	} `json:"message"`
}

func (b *Bot) Start(ctx context.Context, onMessage func(channel.Inbound)) error {
	offset := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		raw, err := b.api(ctx, "getUpdates", map[string]any{
			"timeout":         50,
			"offset":          offset,
			"allowed_updates": []string{"message"},
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			time.Sleep(2 * time.Second)
			continue
		}
		var updates []update
		if err := json.Unmarshal(raw, &updates); err != nil {
			time.Sleep(time.Second)
			continue
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			m := u.Message
			if m == nil {
				continue
			}

			text := m.Text
			if text == "" {
				text = m.Caption
			}

			// Download an attached photo (largest size) or an image document.
			var images []llm.Image
			switch {
			case len(m.Photo) > 0:
				if img, err := b.downloadImage(ctx, m.Photo[len(m.Photo)-1].FileID, "image/jpeg"); err == nil {
					images = append(images, img)
				}
			case m.Document != nil && strings.HasPrefix(m.Document.MimeType, "image/"):
				if img, err := b.downloadImage(ctx, m.Document.FileID, m.Document.MimeType); err == nil {
					images = append(images, img)
				}
			}

			if text == "" && len(images) == 0 {
				continue // unsupported (sticker, voice, etc.)
			}

			from := m.From.Username
			if from == "" {
				from = m.From.FirstName
			}
			onMessage(channel.Inbound{
				ChatID: strconv.FormatInt(m.Chat.ID, 10),
				Text:   text,
				From:   from,
				Images: images,
			})
		}
	}
}

// downloadImage resolves a Telegram file_id to bytes (getFile + download), returning a
// vision-ready image. Size is capped so a huge upload can't exhaust memory.
func (b *Bot) downloadImage(ctx context.Context, fileID, fallbackMime string) (llm.Image, error) {
	res, err := b.api(ctx, "getFile", map[string]any{"file_id": fileID})
	if err != nil {
		return llm.Image{}, err
	}
	var f struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(res, &f); err != nil || f.FilePath == "" {
		return llm.Image{}, fmt.Errorf("telegram getFile: no file_path")
	}

	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.token, f.FilePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return llm.Image{}, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return llm.Image{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8MB cap
	if err != nil {
		return llm.Image{}, err
	}
	return llm.Image{MediaType: imageMime(f.FilePath, fallbackMime), Data: data}, nil
}

// imageMime picks a media type from the file extension, falling back as given.
func imageMime(path, fallback string) string {
	switch {
	case strings.HasSuffix(strings.ToLower(path), ".png"):
		return "image/png"
	case strings.HasSuffix(strings.ToLower(path), ".webp"):
		return "image/webp"
	case strings.HasSuffix(strings.ToLower(path), ".gif"):
		return "image/gif"
	case strings.HasSuffix(strings.ToLower(path), ".jpg"), strings.HasSuffix(strings.ToLower(path), ".jpeg"):
		return "image/jpeg"
	}
	if fallback != "" {
		return fallback
	}
	return "image/jpeg"
}

func (b *Bot) SendText(ctx context.Context, chatID, text string) (string, error) {
	return b.send(ctx, chatID, text, "")
}

func (b *Bot) SendMarkdown(ctx context.Context, chatID, text string) (string, error) {
	return b.send(ctx, chatID, text, "MarkdownV2")
}

func (b *Bot) SendHTML(ctx context.Context, chatID, text string) (string, error) {
	return b.send(ctx, chatID, text, "HTML")
}

func (b *Bot) send(ctx context.Context, chatID, text, parseMode string) (string, error) {
	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	res, err := b.api(ctx, "sendMessage", payload)
	if err != nil {
		return "", err
	}
	var m struct {
		MessageID int `json:"message_id"`
	}
	_ = json.Unmarshal(res, &m)
	return strconv.Itoa(m.MessageID), nil
}

// SetCommands registers the bot's command list so Telegram shows the "/" menu.
func (b *Bot) SetCommands(ctx context.Context, cmds []channel.Command) error {
	tc := make([]map[string]string, 0, len(cmds))
	for _, c := range cmds {
		tc = append(tc, map[string]string{
			"command":     strings.TrimPrefix(c.Name, "/"),
			"description": c.Description,
		})
	}
	_, err := b.api(ctx, "setMyCommands", map[string]any{"commands": tc})
	return err
}

// SendPhoto sends an image. An http(s) URL (or a Telegram file_id) is passed straight
// to the API; anything else is treated as a local file and uploaded via multipart.
func (b *Bot) SendPhoto(ctx context.Context, chatID, photo, caption string) (string, error) {
	var res json.RawMessage
	var err error
	if strings.HasPrefix(photo, "http://") || strings.HasPrefix(photo, "https://") {
		payload := map[string]any{"chat_id": chatID, "photo": photo}
		if caption != "" {
			payload["caption"] = caption
		}
		res, err = b.api(ctx, "sendPhoto", payload)
	} else {
		res, err = b.uploadPhoto(ctx, chatID, photo, caption)
	}
	if err != nil {
		return "", err
	}
	var m struct {
		MessageID int `json:"message_id"`
	}
	_ = json.Unmarshal(res, &m)
	return strconv.Itoa(m.MessageID), nil
}

// uploadPhoto sends a local image file as multipart/form-data.
func (b *Bot) uploadPhoto(ctx context.Context, chatID, path, caption string) (json.RawMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open photo: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("chat_id", chatID)
	if caption != "" {
		_ = w.WriteField("caption", caption)
	}
	fw, err := w.CreateFormFile("photo", filepath.Base(path))
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint("sendPhoto"), &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	return b.do(req, "sendPhoto")
}

func (b *Bot) EditText(ctx context.Context, chatID, msgID, text string) error {
	return b.edit(ctx, chatID, msgID, text, "")
}

func (b *Bot) EditHTML(ctx context.Context, chatID, msgID, text string) error {
	return b.edit(ctx, chatID, msgID, text, "HTML")
}

func (b *Bot) edit(ctx context.Context, chatID, msgID, text, parseMode string) error {
	id, _ := strconv.Atoi(msgID)
	payload := map[string]any{
		"chat_id":    chatID,
		"message_id": id,
		"text":       text,
	}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	_, err := b.api(ctx, "editMessageText", payload)
	return err
}

func (b *Bot) DeleteText(ctx context.Context, chatID, msgID string) error {
	id, _ := strconv.Atoi(msgID)
	_, err := b.api(ctx, "deleteMessage", map[string]any{
		"chat_id":    chatID,
		"message_id": id,
	})
	return err
}

func (b *Bot) SendChatAction(ctx context.Context, chatID, action string) error {
	_, err := b.api(ctx, "sendChatAction", map[string]any{
		"chat_id": chatID,
		"action":  action,
	})
	return err
}
