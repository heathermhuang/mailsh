package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Every scope mail.sh asks for. The consent page leaves sending and calendar writes unticked: the person chooses them.
const requestedScopes = "mailgpt.read mail.read mail.send calendar.read calendar.write maxine.chat offline_access"

type serverMetadata struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	RegistrationEndpoint  string `json:"registration_endpoint"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	Description  string `json:"error_description"`
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func fetchMetadata(api string) (*serverMetadata, error) {
	res, err := httpClient.Get(api + "/.well-known/oauth-authorization-server")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("%s did not describe its sign-in (HTTP %d)", api, res.StatusCode)
	}
	var m serverMetadata
	if err := json.NewDecoder(res.Body).Decode(&m); err != nil || m.AuthorizationEndpoint == "" || m.TokenEndpoint == "" || m.RegistrationEndpoint == "" {
		return nil, fmt.Errorf("%s described its sign-in in a way mail.sh does not understand", api)
	}
	return &m, nil
}

func registerClient(m *serverMetadata, redirectURI string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"client_name":                "mail.sh",
		"redirect_uris":              []string{redirectURI},
		"token_endpoint_auth_method": "none",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
	})
	res, err := httpClient.Post(m.RegistrationEndpoint, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct {
		ClientID string `json:"client_id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || out.ClientID == "" {
		return "", fmt.Errorf("could not register mail.sh with MailGPT (HTTP %d)", res.StatusCode)
	}
	return out.ClientID, nil
}

func postToken(tokenURL string, form url.Values) (*tokenResponse, error) {
	res, err := httpClient.PostForm(tokenURL, form)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var t tokenResponse
	_ = json.NewDecoder(res.Body).Decode(&t)
	if res.StatusCode != 200 || t.AccessToken == "" {
		if t.Description != "" {
			return nil, errors.New(t.Description)
		}
		return nil, fmt.Errorf("sign-in refused (HTTP %d %s)", res.StatusCode, t.Error)
	}
	return &t, nil
}

// openBrowser is a variable so the tests can stand in for the browser.
var openBrowser = func(u string) bool {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start() == nil
}

// codeFromRedirect reads the code out of the URL the sign-in redirected to, after checking it answers this sign-in's state.
func codeFromRedirect(raw, state string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("no URL pasted")
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", errors.New("that is not a URL")
	}
	q := u.Query()
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			return "", errors.New("sign-in cancelled")
		}
		return "", fmt.Errorf("sign-in failed: %s", e)
	}
	if q.Get("state") != state {
		return "", errors.New("that URL is from a different sign-in: run `mail.sh auth` again")
	}
	if q.Get("code") == "" {
		return "", errors.New("that URL carries no sign-in code")
	}
	return q.Get("code"), nil
}

// login runs OAuth with PKCE against a loopback redirect (RFC 8252). With noBrowser, it prints the link and reads back the URL the
// browser lands on, for a machine with no browser (over SSH the 127.0.0.1 page fails to load, but its address carries the code).
func login(api string, noBrowser bool, in io.Reader, out io.Writer) (*Credentials, error) {
	m, err := fetchMetadata(api)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	clientID, err := registerClient(m, redirectURI)
	if err != nil {
		return nil, err
	}
	verifier, state := randomString(32), randomString(16)
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"code_challenge":        {pkceChallenge(verifier)},
		"code_challenge_method": {"S256"},
		"scope":                 {requestedScopes},
		"state":                 {state},
		"resource":              {api},
	}
	authURL := m.AuthorizationEndpoint + "?" + q.Encode()

	var code string
	if noBrowser {
		fmt.Fprintf(out, "Open this link in a browser and sign in to MailGPT:\n\n  %s\n\nThe browser then tries to open a 127.0.0.1 page, which may fail to load. Copy the whole address from the address bar and paste it here:\n> ", authURL)
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && line == "" {
			return nil, errors.New("no URL pasted")
		}
		if code, err = codeFromRedirect(line, state); err != nil {
			return nil, err
		}
	} else {
		got := make(chan string, 1)
		srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/callback" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<!doctype html><meta name="viewport" content="width=device-width"><body style="font:16px system-ui;padding:32px">mail.sh is signed in. You can close this tab and go back to the terminal.</body>`)
			select {
			case got <- r.URL.String():
			default:
			}
		})}
		go srv.Serve(ln)
		defer srv.Shutdown(context.Background())
		fmt.Fprintf(out, "Opening MailGPT in your browser to sign in. If it does not open, go to:\n\n  %s\n\n", authURL)
		openBrowser(authURL)
		select {
		case raw := <-got:
			if code, err = codeFromRedirect(raw, state); err != nil {
				return nil, err
			}
		case <-time.After(10 * time.Minute):
			return nil, errors.New("timed out waiting for the sign-in (try `mail.sh auth --no-browser`)")
		}
	}

	t, err := postToken(m.TokenEndpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"resource":      {api},
	})
	if err != nil {
		return nil, err
	}
	c := &Credentials{API: api, TokenURL: m.TokenEndpoint, ClientID: clientID, AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, ExpiresAt: time.Now().Unix() + t.ExpiresIn}
	if err := saveCredentials(c); err != nil {
		return nil, fmt.Errorf("signed in, but the token could not be saved: %w", err)
	}
	return c, nil
}

// refresh swaps the refresh token for a new access token. A connection lasts at most 30 days, then `mail.sh auth` again.
func refresh(c *Credentials) error {
	if c.RefreshToken == "" {
		return errors.New("signed out: run `mail.sh auth`")
	}
	t, err := postToken(c.TokenURL, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {c.RefreshToken},
		"client_id":     {c.ClientID},
		"resource":      {c.API},
	})
	if err != nil {
		return fmt.Errorf("signed out (%v): run `mail.sh auth`", err)
	}
	c.AccessToken, c.ExpiresAt = t.AccessToken, time.Now().Unix()+t.ExpiresIn
	if t.RefreshToken != "" {
		c.RefreshToken = t.RefreshToken
	}
	if err := saveCredentials(c); err != nil {
		fmt.Fprintln(os.Stderr, "mail.sh: the new token could not be saved:", err)
	}
	return nil
}
