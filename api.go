package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 90 * time.Second}

// APIError is MailGPT's JSON error: {"error": code, "error_description": text}.
type APIError struct {
	Status      int
	Code        string `json:"error"`
	Description string `json:"error_description"`
}

func (e *APIError) Error() string {
	if e.Description != "" {
		return e.Description
	}
	return fmt.Sprintf("MailGPT answered HTTP %d %s", e.Status, e.Code)
}

type client struct{ creds *Credentials }

// do sends one request, refreshing the access token first when it is about to expire, and once more on a 401.
func (c *client) do(method, path string, query url.Values, body any, out any) (int, error) {
	if time.Now().Unix() > c.creds.ExpiresAt-60 {
		if err := refresh(c.creds); err != nil {
			return 0, err
		}
	}
	status, err := c.send(method, path, query, body, out)
	if apiErr, ok := err.(*APIError); ok && apiErr.Status == 401 {
		if rerr := refresh(c.creds); rerr != nil {
			return 0, rerr
		}
		return c.send(method, path, query, body, out)
	}
	return status, err
}

func (c *client) send(method, path string, query url.Values, body any, out any) (int, error) {
	u := c.creds.API + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, u, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.creds.AccessToken)
	req.Header.Set("User-Agent", "mail.sh/"+version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return res.StatusCode, err
	}
	if res.StatusCode >= 400 {
		e := &APIError{Status: res.StatusCode}
		_ = json.Unmarshal(data, e)
		// Every route answers 404 while mail.sh is not open for the account (KV v2:mailsh); a message id that does not exist says so itself.
		if res.StatusCode == 404 && e.Description == "Not found." {
			e.Description = "mail.sh is not open for this MailGPT account yet."
		}
		if e.Code == "insufficient_scope" {
			for scope, box := range scopeBoxes {
				if strings.Contains(e.Description, scope) {
					e.Description = fmt.Sprintf("mail.sh isn't allowed to %s. Run `mail.sh auth` again and tick \"%s\".", box[0], box[1])
				}
			}
		}
		return res.StatusCode, e
	}
	if out != nil {
		if raw, ok := out.(*json.RawMessage); ok {
			*raw = append((*raw)[:0], data...)
			return res.StatusCode, nil
		}
		if err := json.Unmarshal(data, out); err != nil {
			return res.StatusCode, fmt.Errorf("MailGPT's answer could not be read: %w", err)
		}
	}
	return res.StatusCode, nil
}

// scopeBoxes: what each permission does, and its box on MailGPT's consent page.
var scopeBoxes = map[string][2]string{
	"mail.send":      {"send email", "Send email as you"},
	"calendar.write": {"add calendar events", "Add events to your calendar"},
	"mail.read":      {"read your email", "Search and read your email"},
	"calendar.read":  {"see your calendar", "See your calendar"},
	"maxine.chat":    {"talk to Maxine", "Talk to Maxine for you"},
}
