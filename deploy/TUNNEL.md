# Publishing the dashboard through a Cloudflare tunnel

The dashboard holds every invoice, every supplier relationship and the VAT
figures behind a tax return, and its Sync button spends money on the Gemini
API. A tunnel puts all of that on the public internet behind one hostname.
Work through this in order.

## 1. Set a password first

This is not optional — `goldstar serve` refuses to start without one.

```sh
goldstar passwd            # prompts twice, prints the hash line
```

Paste the printed `GOLDSTAR_PASSWORD_HASH=…` line into
the `.env` beside the binary, then add:

```sh
GOLDSTAR_COOKIE_SECURE=true
```

Cloudflare terminates TLS, so session cookies must be marked `Secure` or they
would be sent over a plain connection if anyone reached the origin directly.

Verify before going further:

```sh
goldstar doctor            # should print: DASHBOARD   password set
```

## 2. Keep the app on loopback

Leave `WEB_ADDR=127.0.0.1:8787`. `cloudflared` runs on the same machine and
connects over loopback, so the listener never needs to be exposed. Binding to
`0.0.0.0` would additionally expose it to your LAN for no benefit.

## 3. Install and create the tunnel

```sh
# Debian
curl -L https://pkg.cloudflare.com/cloudflare-main.gpg \
  | sudo tee /usr/share/keyrings/cloudflare-main.gpg >/dev/null
echo 'deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main' \
  | sudo tee /etc/apt/sources.list.d/cloudflared.list
sudo apt update && sudo apt install cloudflared

# CachyOS / Arch
paru -S cloudflared        # or: yay -S cloudflared
```

```sh
cloudflared tunnel login
cloudflared tunnel create goldstar
cloudflared tunnel route dns goldstar invoices.yourdomain.co.uk
```

Copy `cloudflared-config.yml` from this directory to `~/.cloudflared/config.yml`
and substitute the tunnel UUID, your home path and your hostname.

## 4. Put Cloudflare Access in front of it

Strongly recommended. The application password is one lock; Access is a second
one that stops unauthenticated traffic *before* it ever reaches your machine,
so a bug in this app is not the only thing standing between the internet and
your invoices.

In the Cloudflare dashboard: **Zero Trust → Access → Applications → Add a
self-hosted application**, pointed at `invoices.yourdomain.co.uk`, with a policy
allowing only your own email address. Cloudflare then handles the login and
only forwards requests it has already authenticated.

With Access enabled you get two independent gates. Keep both.

## 5. Run both services

```sh
systemctl --user enable --now goldstar-web.service
cloudflared tunnel run goldstar          # test in the foreground first
```

Once it works, install cloudflared's own service so it survives a reboot:

```sh
sudo cloudflared service install
```

Note that `goldstar-web.service` is a **user** unit and cloudflared installs a
**system** unit. The user unit needs lingering enabled, which `install.sh`
already does.

## 6. Check what you actually published

```sh
curl -s -o /dev/null -w '%{http_code}\n' https://invoices.yourdomain.co.uk/
# 200 -> serves the sign-in page (correct)
# 302 -> Cloudflare Access is intercepting (correct, and better)

# The API must never answer without a session:
curl -s https://invoices.yourdomain.co.uk/api/overview
# {"error":"not authenticated"}
```

If that second command returns invoice data, stop the tunnel immediately.

## What is protected, and what is not

Built in:

- Argon2id password hashing, HMAC-signed `HttpOnly` session cookies
- CSRF tokens on every mutating request
- Login rate limiting, keyed on `CF-Connecting-IP` so a tunnel does not
  collapse every visitor into one bucket
- A strict Content-Security-Policy, `X-Frame-Options: DENY`, `nosniff`
- Archived documents served only from inside the attachments directory, looked
  up by database id rather than by a path from the URL

Not built in, and worth knowing:

- **No multi-user accounts.** One shared password. Anyone who has it has
  everything, including the ability to delete records.
- **No audit log.** Deletions and edits leave no trail beyond the archived PDFs.
- **No brute-force lockout beyond rate limiting.** Use a long random password;
  a manager will remember it for you.
- **Invoice PDFs still go to Google** for extraction. The tunnel does not
  change that.
