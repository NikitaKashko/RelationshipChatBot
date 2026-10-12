// Package sandbox provides an owner-only Telegram simulator. Production routing
// and authorization stay unchanged; only this process maps virtual identities.
package sandbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
)

type transport struct {
	owner int64
	next  http.RoundTripper
}

func (t transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.ContentLength == 0 && req.Header.Get("Content-Type") == "" {
		return t.next.RoundTrip(req)
	}
	media, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || media != "multipart/form-data" {
		return nil, errors.New("unexpected simulator request")
	}
	if req.ContentLength > 25<<20 {
		return nil, errors.New("simulator upload too large")
	}
	copyReq := req.Clone(req.Context())
	copyReq.Body = io.NopCloser(io.LimitReader(req.Body, (25<<20)+1))
	if err = copyReq.ParseMultipartForm(1 << 20); err != nil {
		return nil, errors.New("invalid simulator request")
	}
	defer copyReq.MultipartForm.RemoveAll()
	fields := copyReq.MultipartForm.Value
	role := int64(0)
	if chat, ok := fields["chat_id"]; ok {
		if len(chat) != 1 {
			return nil, errors.New("invalid simulator recipient")
		}
		role, err = strconv.ParseInt(chat[0], 10, 64)
		if err != nil || (role != 1 && role != 2 && role != t.owner) {
			return nil, errors.New("simulator recipient denied")
		}
		fields["chat_id"] = []string{strconv.FormatInt(t.owner, 10)}
		label := "[ТЕСТ] "
		if role == 1 {
			label = "[ТЕСТ · А] "
		}
		if role == 2 {
			label = "[ТЕСТ · Б] "
		}
		for _, key := range []string{"text", "caption"} {
			if v := fields[key]; len(v) > 0 {
				fields[key][0] = label + v[0]
			}
		}
		if v := fields["reply_markup"]; len(v) > 0 && (role == 1 || role == 2) {
			v[0], err = markKeyboard(v[0], role)
			if err != nil {
				return nil, err
			}
		}
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for key, values := range fields {
		for _, value := range values {
			if err = w.WriteField(key, value); err != nil {
				return nil, err
			}
		}
	}
	for key, files := range copyReq.MultipartForm.File {
		for _, f := range files {
			src, e := f.Open()
			if e != nil {
				return nil, e
			}
			dst, e := w.CreatePart(f.Header)
			if e == nil {
				_, e = io.Copy(dst, src)
			}
			src.Close()
			if e != nil {
				return nil, e
			}
			_ = key
		}
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	out := req.Clone(req.Context())
	out.Body = io.NopCloser(bytes.NewReader(body.Bytes()))
	out.ContentLength = int64(body.Len())
	out.GetBody = nil
	out.Header.Set("Content-Type", w.FormDataContentType())
	return t.next.RoundTrip(out)
}

func markKeyboard(raw string, role int64) (string, error) {
	var markup map[string]any
	if err := json.Unmarshal([]byte(raw), &markup); err != nil {
		return "", errors.New("invalid simulator keyboard")
	}
	if rows, ok := markup["inline_keyboard"].([]any); ok {
		for _, row := range rows {
			if buttons, ok := row.([]any); ok {
				for _, item := range buttons {
					if button, ok := item.(map[string]any); ok {
						if data, ok := button["callback_data"].(string); ok && data != "" {
							data = fmt.Sprintf("sim:%d:%s", role, data)
							if len(data) > 64 {
								return "", errors.New("simulator callback too long")
							}
							button["callback_data"] = data
						}
					}
				}
			}
		}
	}
	if rows, ok := markup["keyboard"].([]any); ok {
		markup["keyboard"] = append(rows, []any{map[string]any{"text": "🧪 Роль А"}, map[string]any{"text": "🧪 Роль Б"}})
	}
	b, err := json.Marshal(markup)
	return string(b), err
}
