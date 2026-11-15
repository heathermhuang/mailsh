# mail.sh

Your email and calendar from the terminal, for you and for your agents. Gmail and Outlook, through
MailGPT's verified sign-in: no Google Cloud project, no API keys, no client JSON.

```sh
curl -fsSL https://mail.sh/install | sh     # or: brew install heathermhuang/mailsh/mailsh
mail.sh auth
mail.sh list -q "is:unread"
```

> mail.sh is in early access: it works only for MailGPT accounts it has been opened for.

## Commands

```
mail.sh auth [--no-browser]       sign in (opens MailGPT in a browser)
mail.sh logout | whoami

mail.sh list [-q QUERY] [-n N] [--days D]     Gmail search syntax
mail.sh read <id>
mail.sh send --to A[,B] [--cc C] --subject S [--body B | < file] [--reply-to <id>]

mail.sh cal today | cal list [--days N]
mail.sh cal add --title T --start 2026-11-20T15:00 --end 2026-11-20T16:00 [--location L]

mail.sh triage [--hours N] [--pushed]     what Maxine did with recent email
mail.sh waiting                            emails she is waiting for
mail.sh owe                                what you owe, and are owed
mail.sh ask "did Ana send the contract?"   ask Maxine (she only reads here)
mail.sh maxine on                          turn Maxine on for a mail.sh-only account
```

Every command takes `--json` for scripts and agents.

## Nothing is sent without you

`send`, `cal add` and `maxine on` only ask. Each prints a link to a MailGPT page where you,
signed in, read exactly what will happen and tap to approve it. Nothing the CLI or an agent sends
can skip that page: an agent reading your email can be tricked by what it reads, so the server,
not the client, decides. Sending and adding events are also separate permissions you tick when you
sign in, and both have daily limits.

## Accounts

A new account made through mail.sh is mail.sh-only: MailGPT passes your commands to Gmail or
Outlook and runs no AI on your mail. `mail.sh maxine on` turns on Maxine, MailGPT's assistant:
she triages new mail, keeps track of what you owe and are owed, answers `ask`, and alerts you in
Telegram, WhatsApp or Slack. An existing MailGPT account already has her.

## Sign-in and storage

`mail.sh auth` registers this CLI with MailGPT and signs in with OAuth (PKCE, a 127.0.0.1
redirect). On a machine with no browser, `--no-browser` prints the link and takes back the address
the browser lands on. Tokens are kept in the macOS keychain, elsewhere in
`~/.config/mailsh/credentials.json` (mode 0600). Access tokens last an hour and a connection 30
days; end it any time in MailGPT Settings → Connected AI apps.

## Build

```sh
go test ./...
go build -o mail.sh .
```

MIT licensed.
