// mail.sh: email and calendar from the terminal, through MailGPT's hosted sign-in (no Google Cloud project of your own).
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

var version = "dev"

const defaultAPI = "https://mailgpt.com"

const usage = `mail.sh: your email and calendar from the terminal, through MailGPT.

  mail.sh auth [--no-browser]       sign in (opens MailGPT in a browser)
  mail.sh logout | whoami

  mail.sh list [-q QUERY] [-n N] [--days D]
  mail.sh read <id>
  mail.sh send --to A[,B] [--cc C] --subject S [--body B | < file] [--reply-to <id>]

  mail.sh cal today | cal list [--days N]
  mail.sh cal add --title T --start 2026-11-20T15:00 --end 2026-11-20T16:00 [--location L]
                  (local times in your timezone, or dates like 2026-11-20 for all day)

  mail.sh triage [--hours N] [--pushed]     what Maxine did with recent email
  mail.sh waiting                            emails she is waiting for
  mail.sh owe                                what you owe, and are owed
  mail.sh ask "..."                          ask Maxine (she only reads here)
  mail.sh maxine on                          turn Maxine on for a mail.sh-only account

Every command takes --json. Sending, adding an event and turning Maxine on wait for your tap on
a MailGPT page, signed in, before anything happens. MAILSH_API sets the server (default ` + defaultAPI + `).
`

type options struct {
	json      bool
	noBrowser bool
	api       string
	out       io.Writer
	in        io.Reader
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mail.sh:", err)
		os.Exit(1)
	}
}

// run takes the global flags from anywhere in the arguments, then runs the command.
func run(args []string, in io.Reader, out io.Writer) error {
	o := &options{api: strings.TrimRight(envOr("MAILSH_API", defaultAPI), "/"), out: out, in: in}
	var rest []string
	for _, a := range args {
		switch a {
		case "--json":
			o.json = true
		case "--no-browser":
			o.noBrowser = true
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 || rest[0] == "help" || rest[0] == "-h" || rest[0] == "--help" {
		fmt.Fprint(out, usage)
		return nil
	}
	cmd, args := rest[0], rest[1:]
	switch cmd {
	case "version", "--version":
		fmt.Fprintln(out, "mail.sh", version)
		return nil
	case "auth", "login":
		return cmdAuth(o)
	case "logout":
		if err := deleteCredentials(o.api); err != nil {
			return err
		}
		fmt.Fprintln(out, "Signed out on this computer. To end the connection everywhere, disconnect mail.sh in MailGPT Settings → Connected AI apps.")
		return nil
	}
	creds, err := loadCredentials(o.api)
	if err != nil {
		return err
	}
	c := &client{creds: creds}
	switch cmd {
	case "whoami":
		return cmdWhoami(o, c)
	case "list":
		return cmdList(o, c, args)
	case "read":
		return cmdRead(o, c, args)
	case "send":
		return cmdSend(o, c, args)
	case "cal":
		return cmdCal(o, c, args)
	case "triage":
		return cmdTriage(o, c, args)
	case "waiting":
		return cmdWaiting(o, c, args)
	case "owe":
		return cmdOwe(o, c, args)
	case "ask":
		return cmdAsk(o, c, args)
	case "maxine":
		if len(args) != 1 || args[0] != "on" {
			return errors.New("usage: mail.sh maxine on")
		}
		return cmdMaxineOn(o, c)
	}
	return fmt.Errorf("unknown command %q (mail.sh help)", cmd)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// get fetches a route; with --json it prints MailGPT's answer as it came and returns nil data.
func get(o *options, c *client, path string, q url.Values, method string, body any) (json.RawMessage, error) {
	var raw json.RawMessage
	if _, err := c.do(method, path, q, body, &raw); err != nil {
		return nil, err
	}
	if o.json {
		fmt.Fprintln(o.out, string(raw))
		return nil, nil
	}
	return raw, nil
}

func parse(name string, args []string, fs *flag.FlagSet) ([]string, error) {
	fs.SetOutput(io.Discard)
	// Flags may come after the positional arguments too.
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	return pos, nil
}

func cmdAuth(o *options) error {
	creds, err := login(o.api, o.noBrowser, o.in, o.out)
	if err != nil {
		return err
	}
	c := &client{creds: creds}
	var me meView
	if _, err := c.do("GET", "/v1/me", nil, nil, &me); err != nil {
		fmt.Fprintln(o.out, "Signed in.")
		return err
	}
	fmt.Fprintf(o.out, "Signed in as %s.\n", me.Email)
	printScopes(o.out, me.Scopes)
	return nil
}

type meView struct {
	Email     string   `json:"email"`
	Mode      string   `json:"mode"`
	Timezone  string   `json:"timezone"`
	Scopes    []string `json:"scopes"`
	Mailboxes []struct {
		Email    string `json:"email"`
		Provider string `json:"provider"`
	} `json:"mailboxes"`
}

func printScopes(w io.Writer, scopes []string) {
	has := map[string]bool{}
	for _, s := range scopes {
		has[s] = true
	}
	if !has["mail.send"] {
		fmt.Fprintln(w, "Sending is not allowed: run `mail.sh auth` again and tick \"Send email as you\" to use `mail.sh send`.")
	}
	if !has["calendar.write"] {
		fmt.Fprintln(w, "Adding events is not allowed: run `mail.sh auth` again and tick it to use `mail.sh cal add`.")
	}
}

func cmdWhoami(o *options, c *client) error {
	raw, err := get(o, c, "/v1/me", nil, "GET", nil)
	if raw == nil {
		return err
	}
	var me meView
	if err := json.Unmarshal(raw, &me); err != nil {
		return err
	}
	mode := "Maxine on"
	if me.Mode == "cli" {
		mode = "mail.sh only (Maxine off: `mail.sh maxine on`)"
	}
	fmt.Fprintf(o.out, "%s · %s · %s\n", me.Email, mode, me.Timezone)
	for _, m := range me.Mailboxes {
		fmt.Fprintf(o.out, "  mailbox: %s (%s)\n", m.Email, m.Provider)
	}
	fmt.Fprintf(o.out, "  allowed: %s\n", strings.Join(me.Scopes, " "))
	return nil
}

type messageView struct {
	ID       string  `json:"id"`
	ThreadID *string `json:"thread_id"`
	From     *string `json:"from"`
	To       *string `json:"to"`
	Cc       *string `json:"cc"`
	Subject  string  `json:"subject"`
	Date     *string `json:"date"`
	Snippet  string  `json:"snippet"`
	Body     string  `json:"body"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func cmdList(o *options, c *client, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	q := fs.String("q", "", "Gmail search")
	n := fs.Int("n", 20, "how many")
	days := fs.Int("days", 30, "how far back")
	if _, err := parse("list", args, fs); err != nil {
		return err
	}
	raw, err := get(o, c, "/v1/messages", url.Values{"q": {*q}, "limit": {strconv.Itoa(*n)}, "days": {strconv.Itoa(*days)}}, "GET", nil)
	if raw == nil {
		return err
	}
	var r struct {
		Messages []messageView `json:"messages"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if len(r.Messages) == 0 {
		fmt.Fprintln(o.out, "No messages.")
	}
	for _, m := range r.Messages {
		fmt.Fprintf(o.out, "%s  %-16s  %-28s  %s\n", m.ID, shortDate(str(m.Date)), clip(str(m.From), 28), clip(m.Subject, 70))
	}
	return nil
}

func cmdRead(o *options, c *client, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: mail.sh read <id>")
	}
	raw, err := get(o, c, "/v1/messages/"+url.PathEscape(args[0]), nil, "GET", nil)
	if raw == nil {
		return err
	}
	var m messageView
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	fmt.Fprintf(o.out, "From: %s\nTo: %s\n", str(m.From), str(m.To))
	if str(m.Cc) != "" {
		fmt.Fprintf(o.out, "Cc: %s\n", str(m.Cc))
	}
	fmt.Fprintf(o.out, "Date: %s\nSubject: %s\n\n%s\n", str(m.Date), m.Subject, m.Body)
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func cmdSend(o *options, c *client, args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	to := fs.String("to", "", "recipients, comma separated")
	cc := fs.String("cc", "", "cc, comma separated")
	subject := fs.String("subject", "", "subject")
	body := fs.String("body", "", "body (or pipe it in)")
	replyTo := fs.String("reply-to", "", "message id this replies to")
	if _, err := parse("send", args, fs); err != nil {
		return err
	}
	if *to == "" {
		return errors.New("usage: mail.sh send --to A[,B] --subject S [--body B | < file]")
	}
	text := *body
	if text == "" {
		if f, ok := o.in.(*os.File); !ok || !isTerminal(f) {
			data, err := io.ReadAll(io.LimitReader(o.in, 128<<10))
			if err != nil {
				return err
			}
			text = string(data)
		}
	}
	req := map[string]any{"to": splitList(*to), "subject": *subject, "body": text}
	if *cc != "" {
		req["cc"] = splitList(*cc)
	}
	if *replyTo != "" {
		req["in_reply_to"] = *replyTo
	}
	raw, err := get(o, c, "/v1/messages", nil, "POST", req)
	if raw == nil {
		return err
	}
	return waitForTap(o, raw, "Nothing is sent yet.")
}

// waitForTap shows the approval link a write answers with, and opens it in the browser.
func waitForTap(o *options, raw json.RawMessage, nothingYet string) error {
	var r struct {
		Status        string   `json:"status"`
		ApprovalURL   string   `json:"approval_url"`
		NewRecipients []string `json:"new_recipients"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if r.Status == "already_on" {
		fmt.Fprintln(o.out, "Maxine is already on for this account.")
		return nil
	}
	fmt.Fprintf(o.out, "%s Approve it, signed in to MailGPT:\n\n  %s\n", nothingYet, r.ApprovalURL)
	if len(r.NewRecipients) > 0 {
		fmt.Fprintf(o.out, "\nNever written to before: %s\n", strings.Join(r.NewRecipients, ", "))
	}
	if !o.noBrowser {
		openBrowser(r.ApprovalURL)
	}
	return nil
}

type eventView struct {
	Title    string  `json:"title"`
	Start    string  `json:"start"`
	End      string  `json:"end"`
	AllDay   bool    `json:"all_day"`
	Location *string `json:"location"`
}

func cmdCal(o *options, c *client, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: mail.sh cal today | cal list [--days N] | cal add ...")
	}
	switch args[0] {
	case "today", "list":
		days := 1
		if args[0] == "list" {
			fs := flag.NewFlagSet("cal list", flag.ContinueOnError)
			d := fs.Int("days", 7, "how many days")
			if _, err := parse("cal list", args[1:], fs); err != nil {
				return err
			}
			days = *d
		}
		var me meView
		if _, err := c.do("GET", "/v1/me", nil, nil, &me); err != nil {
			return err
		}
		loc, err := time.LoadLocation(me.Timezone)
		if err != nil {
			loc = time.Local
		}
		now := time.Now().In(loc)
		from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		to := from.AddDate(0, 0, days)
		raw, err := get(o, c, "/v1/events", url.Values{"from": {from.Format(time.RFC3339)}, "to": {to.Format(time.RFC3339)}}, "GET", nil)
		if raw == nil {
			return err
		}
		var r struct {
			Events []eventView `json:"events"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		if len(r.Events) == 0 {
			fmt.Fprintln(o.out, "Nothing on the calendar.")
		}
		for _, e := range r.Events {
			when := shortDate(e.Start) + "–" + clockOf(e.End)
			if e.AllDay {
				when = shortDay(e.Start) + " all day"
			}
			line := fmt.Sprintf("%-24s  %s", when, e.Title)
			if l := str(e.Location); l != "" {
				line += "  (" + clip(l, 40) + ")"
			}
			fmt.Fprintln(o.out, line)
		}
		return nil
	case "add":
		fs := flag.NewFlagSet("cal add", flag.ContinueOnError)
		title := fs.String("title", "", "title")
		start := fs.String("start", "", "start")
		end := fs.String("end", "", "end")
		location := fs.String("location", "", "location")
		description := fs.String("description", "", "description")
		if _, err := parse("cal add", args[1:], fs); err != nil {
			return err
		}
		if *title == "" || *start == "" || *end == "" {
			return errors.New("usage: mail.sh cal add --title T --start 2026-11-20T15:00 --end 2026-11-20T16:00")
		}
		req := map[string]any{"title": *title, "start": *start, "end": *end}
		if *location != "" {
			req["location"] = *location
		}
		if *description != "" {
			req["description"] = *description
		}
		raw, err := get(o, c, "/v1/events", nil, "POST", req)
		if raw == nil {
			return err
		}
		return waitForTap(o, raw, "Nothing is added yet.")
	}
	return fmt.Errorf("unknown cal command %q", args[0])
}

func cmdTriage(o *options, c *client, args []string) error {
	fs := flag.NewFlagSet("triage", flag.ContinueOnError)
	hours := fs.Int("hours", 24, "how far back")
	pushed := fs.Bool("pushed", false, "only the emails she alerted you about")
	text := fs.String("text", "", "only emails mentioning this")
	n := fs.Int("n", 20, "how many")
	if _, err := parse("triage", args, fs); err != nil {
		return err
	}
	q := url.Values{"hours": {strconv.Itoa(*hours)}, "limit": {strconv.Itoa(*n)}}
	if *pushed {
		q.Set("outcome", "pushed")
	}
	if *text != "" {
		q.Set("text", *text)
	}
	raw, err := get(o, c, "/v1/triage", q, "GET", nil)
	if raw == nil {
		return err
	}
	var r struct {
		Counts    map[string]int `json:"counts"`
		Decisions []struct {
			From    string `json:"from"`
			Subject string `json:"subject"`
			Outcome string `json:"outcome"`
			Alert   string `json:"alert"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	fmt.Fprintf(o.out, "Last %dh: %d pushed, %d for the brief, %d skipped, %d held\n\n", *hours, r.Counts["pushed"], r.Counts["brief"], r.Counts["skipped"], r.Counts["held"])
	for _, d := range r.Decisions {
		fmt.Fprintf(o.out, "%-10s  %-28s  %s\n", clip(d.Outcome, 10), clip(d.From, 28), clip(d.Subject, 70))
		if d.Alert != "" {
			fmt.Fprintf(o.out, "            %s\n", clip(d.Alert, 110))
		}
	}
	return nil
}

func cmdWaiting(o *options, c *client, args []string) error {
	fs := flag.NewFlagSet("waiting", flag.ContinueOnError)
	all := fs.Bool("all", false, "also tasks closed in the last 30 days")
	if _, err := parse("waiting", args, fs); err != nil {
		return err
	}
	q := url.Values{}
	if *all {
		q.Set("include_closed", "true")
	}
	raw, err := get(o, c, "/v1/waiting", q, "GET", nil)
	if raw == nil {
		return err
	}
	var r struct {
		Tasks []struct {
			WaitingFor string `json:"waiting_for"`
			From       string `json:"from"`
			Action     string `json:"action"`
			Deadline   string `json:"deadline"`
			Status     string `json:"status"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if len(r.Tasks) == 0 {
		fmt.Fprintln(o.out, "Maxine is not waiting for any email.")
	}
	for _, t := range r.Tasks {
		line := "• " + t.WaitingFor
		if t.From != "" {
			line += " (from " + t.From + ")"
		}
		if t.Deadline != "" {
			line += " · by " + t.Deadline
		}
		if t.Status != "open" {
			line += " · " + t.Status
		}
		fmt.Fprintln(o.out, line)
	}
	return nil
}

type obligation struct {
	What string `json:"what"`
	Who  string `json:"who"`
	Due  string `json:"due"`
}

func cmdOwe(o *options, c *client, args []string) error {
	raw, err := get(o, c, "/v1/obligations", nil, "GET", nil)
	if raw == nil {
		return err
	}
	var r struct {
		IOwe     []obligation `json:"i_owe"`
		OwedToMe []obligation `json:"owed_to_me"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	section := func(title string, items []obligation) {
		fmt.Fprintln(o.out, title)
		if len(items) == 0 {
			fmt.Fprintln(o.out, "  nothing noticed")
		}
		for _, it := range items {
			line := "  • " + it.What
			if it.Who != "" {
				line += " (" + it.Who + ")"
			}
			if it.Due != "" {
				line += " · due " + it.Due
			}
			fmt.Fprintln(o.out, line)
		}
	}
	section("You owe:", r.IOwe)
	section("Owed to you:", r.OwedToMe)
	return nil
}

func cmdAsk(o *options, c *client, args []string) error {
	msg := strings.TrimSpace(strings.Join(args, " "))
	if msg == "" {
		return errors.New(`usage: mail.sh ask "did Ana send the contract?"`)
	}
	raw, err := get(o, c, "/v1/chat", nil, "POST", map[string]string{"message": msg})
	if raw == nil {
		return err
	}
	var r struct {
		Replies []struct {
			Text string `json:"text"`
		} `json:"replies"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return err
	}
	for _, rep := range r.Replies {
		fmt.Fprintln(o.out, rep.Text)
	}
	return nil
}

func cmdMaxineOn(o *options, c *client) error {
	raw, err := get(o, c, "/v1/maxine", nil, "POST", nil)
	if raw == nil {
		return err
	}
	return waitForTap(o, raw, "Maxine is not on yet.")
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// parseTime reads the times MailGPT answers with: RFC 3339, an RFC 2822 mail date, or a bare date.
func parseTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, time.RFC1123Z, time.RFC1123, "Mon, 2 Jan 2006 15:04:05 -0700", "2 Jan 2006 15:04:05 -0700", "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, strings.TrimSpace(strings.SplitN(s, " (", 2)[0])); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func shortDate(s string) string {
	if t, ok := parseTime(s); ok {
		return t.Format("Mon 2 Jan 15:04")
	}
	return clip(s, 16)
}

func shortDay(s string) string {
	if t, ok := parseTime(s); ok {
		return t.Format("Mon 2 Jan")
	}
	return clip(s, 10)
}

func clockOf(s string) string {
	if t, ok := parseTime(s); ok {
		return t.Format("15:04")
	}
	return s
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
