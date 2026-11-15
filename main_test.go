package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeMailGPT answers the sign-in and the /v1 routes the way MailGPT does. tokens counts each access token it issued.
type fakeMailGPT struct {
	*httptest.Server
	mu       sync.Mutex
	issued   int
	posted   []map[string]any
	expired  bool // the next /v1 call answers 401 once
	scopes   string
	lastAuth url.Values
	decided  []string // "confirm:<id>" or "cancel:<id>", in order
}

func newFake(t *testing.T) *fakeMailGPT {
	f := &fakeMailGPT{scopes: "mailgpt.read mail.read calendar.read maxine.chat offline_access"}
	mux := http.NewServeMux()
	f.Server = httptest.NewServer(mux)
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint": f.URL + "/authorize", "token_endpoint": f.URL + "/token", "registration_endpoint": f.URL + "/register",
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		uris := body["redirect_uris"].([]any)
		if !strings.HasPrefix(uris[0].(string), "http://127.0.0.1:") {
			t.Errorf("redirect uri %v", uris[0])
		}
		json.NewEncoder(w).Encode(map[string]string{"client_id": "cid"})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		f.lastAuth = r.URL.Query()
		q := r.URL.Query()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code=c0de&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code") != "c0de" || r.Form.Get("code_verifier") == "" || pkceChallenge(r.Form.Get("code_verifier")) != f.lastAuth.Get("code_challenge") {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "bad code"})
				return
			}
		case "refresh_token":
			if r.Form.Get("refresh_token") != "r1" {
				w.WriteHeader(400)
				return
			}
		}
		f.mu.Lock()
		f.issued++
		n := f.issued
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"access_token": "a" + string(rune('0'+n)), "refresh_token": "r1", "expires_in": 3600, "scope": f.scopes})
	})
	v1 := func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		expired := f.expired
		f.expired = false
		f.mu.Unlock()
		if expired || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer a") {
			w.WriteHeader(401)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_token", "error_description": "A valid access token is required."})
			return
		}
		switch {
		case r.URL.Path == "/v1/me":
			json.NewEncoder(w).Encode(map[string]any{"email": "sam@example.com", "mode": "full", "timezone": "UTC", "scopes": strings.Fields(f.scopes), "mailboxes": []any{map[string]string{"email": "sam@example.com", "provider": "gmail"}}})
		case strings.HasPrefix(r.URL.Path, "/v1/pending/") && r.Method == "POST":
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/pending/"), "/")
			if !strings.Contains(f.scopes, "mail.send") {
				w.WriteHeader(403)
				json.NewEncoder(w).Encode(map[string]string{"error": "insufficient_scope", "error_description": "This needs the mail.send permission."})
				return
			}
			f.mu.Lock()
			f.decided = append(f.decided, parts[1]+":"+parts[0])
			f.mu.Unlock()
			msg := map[string]string{"confirm": "Sent to ana@example.com.", "cancel": "Nothing was sent."}[parts[1]]
			json.NewEncoder(w).Encode(map[string]string{"status": map[string]string{"confirm": "sent", "cancel": "cancelled"}[parts[1]], "message": msg})
		case r.URL.Path == "/v1/messages" && r.Method == "POST", r.URL.Path == "/v1/events" && r.Method == "POST", r.URL.Path == "/v1/maxine":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.posted = append(f.posted, body)
			f.mu.Unlock()
			w.WriteHeader(202)
			// As MailGPT answers: an address containing "new@" is one the mailbox never wrote to, and then there is no confirm route.
			fresh := []string{}
			if to, ok := body["to"].([]any); ok {
				for _, a := range to {
					if strings.Contains(a.(string), "new@") {
						fresh = append(fresh, a.(string))
					}
				}
			}
			staged := map[string]any{"status": "pending_approval", "id": "pend1", "approval_url": "https://my.example/mailsh/approve?id=x", "new_recipients": fresh}
			if len(fresh) == 0 && r.URL.Path != "/v1/maxine" {
				staged["confirm"] = "POST /v1/pending/pend1/confirm"
			}
			json.NewEncoder(w).Encode(staged)
		case r.URL.Path == "/v1/messages":
			json.NewEncoder(w).Encode(map[string]any{"messages": []any{map[string]any{"id": "m1", "from": "Ana <ana@example.com>", "subject": "Contract", "date": "Tue, 10 Nov 2026 09:00:00 +0000"}}})
		case r.URL.Path == "/v1/triage":
			json.NewEncoder(w).Encode(map[string]any{"counts": map[string]int{"pushed": 1}, "decisions": []any{map[string]string{"from": "ana@example.com", "subject": "Contract", "outcome": "pushed", "alert": "Ana sent the contract"}}})
		case r.URL.Path == "/v1/chat":
			json.NewEncoder(w).Encode(map[string]any{"replies": []any{map[string]string{"text": "Yes, on Tuesday."}}})
		default:
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"error": "not_found", "error_description": "Not found."})
		}
	}
	mux.HandleFunc("/v1/", v1)
	t.Cleanup(f.Close)
	return f
}

// signIn runs `mail.sh auth` against the fake, with a browser that follows the sign-in's redirects.
func signIn(t *testing.T, f *fakeMailGPT) {
	t.Helper()
	t.Setenv("MAILSH_API", f.URL)
	t.Setenv("MAILSH_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "creds.json"))
	openBrowser = func(u string) bool {
		if !strings.HasPrefix(u, f.URL+"/authorize") {
			return false
		}
		go func() {
			res, err := http.Get(u)
			if err == nil {
				io.Copy(io.Discard, res.Body)
				res.Body.Close()
			}
		}()
		return true
	}
	var out bytes.Buffer
	if err := run([]string{"auth"}, strings.NewReader(""), &out); err != nil {
		t.Fatalf("auth: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Signed in as sam@example.com") {
		t.Fatalf("auth output: %s", out.String())
	}
	if f.lastAuth.Get("code_challenge_method") != "S256" || f.lastAuth.Get("scope") != requestedScopes || f.lastAuth.Get("resource") != f.URL {
		t.Fatalf("authorize query: %v", f.lastAuth)
	}
}

func runOut(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(args, strings.NewReader(""), &out)
	return out.String(), err
}

func TestSignInThenRead(t *testing.T) {
	f := newFake(t)
	signIn(t, f)
	out, err := runOut(t, "list", "-n", "5")
	if err != nil || !strings.Contains(out, "m1") || !strings.Contains(out, "Contract") {
		t.Fatalf("list: %v %q", err, out)
	}
	out, err = runOut(t, "triage", "--pushed")
	if err != nil || !strings.Contains(out, "1 pushed") || !strings.Contains(out, "Ana sent the contract") {
		t.Fatalf("triage: %v %q", err, out)
	}
	out, err = runOut(t, "ask", "did", "Ana", "send", "it?")
	if err != nil || strings.TrimSpace(out) != "Yes, on Tuesday." {
		t.Fatalf("ask: %v %q", err, out)
	}
	out, err = runOut(t, "--json", "whoami")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("whoami --json: %v %q", err, out)
	}
	// The scopes it did not get are named after sign-in.
	if !strings.Contains(out, "sam@example.com") {
		t.Fatal(out)
	}
}

func TestRefreshesOnceOn401(t *testing.T) {
	f := newFake(t)
	signIn(t, f)
	f.expired = true
	if _, err := runOut(t, "list"); err != nil {
		t.Fatalf("list after expiry: %v", err)
	}
	if f.issued != 2 {
		t.Fatalf("tokens issued %d, want 2 (sign-in + one refresh)", f.issued)
	}
}

func TestSendShowsTheEmailAndConfirmsInTheTerminal(t *testing.T) {
	f := newFake(t)
	f.scopes += " mail.send calendar.write"
	signIn(t, f)
	ttyAnswer := func(a string) { openTTY = func() io.Reader { return strings.NewReader(a) } }
	defer func() { openTTY = func() io.Reader { return nil } }()

	// y at the prompt: shown, then confirmed once.
	ttyAnswer("y\n")
	var out bytes.Buffer
	if err := run([]string{"send", "--to", "ana@example.com, bo@example.com", "--subject", "Hi"}, strings.NewReader("Body from stdin\n"), &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"To: ana@example.com, bo@example.com", "Subject: Hi", "Body from stdin", "Send? [y/N]", "Sent to ana@example.com."} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
	if got := f.posted[0]; got["body"] != "Body from stdin\n" || len(got["to"].([]any)) != 2 {
		t.Fatalf("posted %v", got)
	}
	if strings.Join(f.decided, ",") != "confirm:pend1" {
		t.Fatalf("decided %v", f.decided)
	}

	// Anything but y cancels it.
	ttyAnswer("\n")
	out2, err := runOut(t, "send", "--to", "ana@example.com", "--subject", "x", "--body", "y")
	if err != nil || !strings.Contains(out2, "Cancelled") || f.decided[len(f.decided)-1] != "cancel:pend1" {
		t.Fatalf("no: %v %q %v", err, out2, f.decided)
	}

	// No terminal (a script, an agent): it prints the confirm command and decides nothing.
	openTTY = func() io.Reader { return nil }
	n := len(f.decided)
	out3, err := runOut(t, "send", "--to", "ana@example.com", "--subject", "x", "--body", "y")
	if err != nil || !strings.Contains(out3, "mail.sh confirm pend1") || len(f.decided) != n {
		t.Fatalf("no tty: %v %q %v", err, out3, f.decided)
	}
	// ...and the confirm command finishes it.
	out4, err := runOut(t, "confirm", "pend1")
	if err != nil || !strings.Contains(out4, "Sent to") || f.decided[len(f.decided)-1] != "confirm:pend1" {
		t.Fatalf("confirm: %v %q", err, out4)
	}

	// --yes confirms without asking; cal add shows the event and goes the same way.
	out5, err := runOut(t, "--yes", "cal", "add", "--title", "Lunch", "--start", "2026-11-20T12:00", "--end", "2026-11-20T13:00")
	if err != nil || !strings.Contains(out5, "Lunch") || !strings.Contains(out5, "No one is invited.") || strings.Contains(out5, "[y/N]") {
		t.Fatalf("cal add --yes: %v %q", err, out5)
	}
	if f.posted[len(f.posted)-1]["title"] != "Lunch" || f.decided[len(f.decided)-1] != "confirm:pend1" {
		t.Fatalf("cal add: %v %v", f.posted, f.decided)
	}
}

// An email to someone the mailbox never wrote to has no confirm route: mail.sh shows the approval link instead of asking, even with --yes.
func TestSendToSomeoneNewWaitsForTheTap(t *testing.T) {
	f := newFake(t)
	f.scopes += " mail.send"
	signIn(t, f)
	openTTY = func() io.Reader { return strings.NewReader("y\n") }
	defer func() { openTTY = func() io.Reader { return nil } }()
	for _, args := range [][]string{
		{"send", "--to", "ana@example.com, new@x.example", "--subject", "Hi", "--body", "x"},
		{"--yes", "send", "--to", "new@x.example", "--subject", "Hi", "--body", "x"},
	} {
		out, err := runOut(t, args...)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Subject: Hi", "never written to", "https://my.example/mailsh/approve?id=x", "Never written to before: new@x.example"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%v: missing %q in:\n%s", args, want, out)
			}
		}
		if strings.Contains(out, "[y/N]") || len(f.decided) != 0 {
			t.Fatalf("%v: asked or decided: %q %v", args, out, f.decided)
		}
	}
}

func TestMissingPermissionSaysWhatToTick(t *testing.T) {
	f := newFake(t)
	signIn(t, f)
	_, err := runOut(t, "confirm", "pend1")
	if err == nil || !strings.Contains(err.Error(), `tick "Send email as you"`) {
		t.Fatalf("err %v", err)
	}
}

func TestNotOpenAndSignedOut(t *testing.T) {
	f := newFake(t)
	signIn(t, f)
	if _, err := runOut(t, "waiting"); err == nil || !strings.Contains(err.Error(), "not open for this MailGPT account") {
		t.Fatalf("waiting: %v", err)
	}
	if _, err := runOut(t, "logout"); err != nil {
		t.Fatal(err)
	}
	if _, err := runOut(t, "list"); err != errNoCredentials {
		t.Fatalf("after logout: %v", err)
	}
}

func TestCodeFromRedirect(t *testing.T) {
	if _, err := codeFromRedirect("http://127.0.0.1:1/callback?code=x&state=other", "mine"); err == nil {
		t.Fatal("a different sign-in's state was accepted")
	}
	if _, err := codeFromRedirect("http://127.0.0.1:1/callback?error=access_denied&state=mine", "mine"); err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("denied: %v", err)
	}
	if c, err := codeFromRedirect("  http://127.0.0.1:1/callback?code=x&state=mine\n", "mine"); err != nil || c != "x" {
		t.Fatalf("good: %q %v", c, err)
	}
}

func TestPasteModeSignIn(t *testing.T) {
	f := newFake(t)
	t.Setenv("MAILSH_API", f.URL)
	t.Setenv("MAILSH_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "creds.json"))
	pr, pw := io.Pipe()
	var out syncBuffer
	done := make(chan error, 1)
	go func() { done <- run([]string{"auth", "--no-browser"}, pr, &out) }()
	// Read the printed link, follow it as a browser would (without following the last redirect), and paste where it lands.
	var link string
	for link == "" {
		for _, l := range strings.Split(out.String(), "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), f.URL+"/authorize") {
				link = strings.TrimSpace(l)
			}
		}
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := noFollow.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	pw.Write([]byte(res.Header.Get("Location") + "\n"))
	if err := <-done; err != nil {
		t.Fatalf("paste sign-in: %v", err)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestFlagsAfterPositionals(t *testing.T) {
	fs := flagSetFor()
	pos, err := parse("x", []string{"pos", "-n", "3"}, fs)
	if err != nil || len(pos) != 1 || pos[0] != "pos" || fs.Lookup("n").Value.String() != "3" {
		t.Fatalf("%v %v", pos, err)
	}
}

func flagSetFor() *flag.FlagSet {
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	fs.Int("n", 1, "")
	return fs
}
