# Garage Goldstar — invoice collector

Pulls supplier invoices out of a Hostinger mailbox, reads the vehicle
registration, part numbers and VAT figures off each PDF, stores everything in a
local SQLite database, and writes an Excel workbook every day.

Single static Go binary. Runs identically on Debian and CachyOS.

## What it extracts

Per invoice: supplier, invoice number, date of purchase, vehicle registration,
currency, netto, VAT rate, VAT amount, brutto.

Per line item: part number, description, quantity, unit price, netto, VAT,
brutto, and a line-level registration when a single invoice covers more than
one vehicle.

Every workbook is sorted by vehicle registration, then by price with the
dearest first, so a plate's costs sit together with the big numbers at the top.
General workshop stock has no plate and sorts to the end. Alongside the
Invoices, Items and Summary sheets there is a **Charts** sheet carrying native
Excel charts — spend by vehicle, by supplier and by month — which stay live and
redraw if you edit or filter the data.

VAT is read off the document, never assumed — a 5% or zero-rated line comes
through as printed rather than being forced to 20%.

Not every invoice belongs to a vehicle. Workshop consumables — WD-40, bulk oil,
brake cleaner, gloves, rags — are detected as **general stock**, kept out of
per-vehicle costs, and not flagged for a missing registration. An invoice that
names no vehicle *and* is not general stock still gets flagged, because that is
genuinely ambiguous.

## Install

```sh
./deploy/install.sh
```

That builds the binary to `~/.local/bin`, installs a systemd **user** timer that
runs daily at 18:30 London time, and creates `.env` beside the binary.

Then fill in the credentials:

```sh
$EDITOR .env   # IMAP_USER, IMAP_PASS, GEMINI_API_KEY
goldstar doctor                          # verifies config + mailbox login
```

## Commands

| Command | What it does |
|---|---|
| `goldstar` | Opens the dashboard. Contacts nothing until you press a button. |
| `goldstar run` | Fetch new invoices, then write today's Excel export. This is the daily job. |
| `goldstar fetch` | Fetch and extract only. |
| `goldstar ingest FILE…` | Extract from local PDFs or photos — scanned paper invoices, or files saved out of a mail client. |
| `goldstar export` | Rebuild the workbook from stored data. |
| `goldstar serve` | Dashboard on <http://127.0.0.1:8787>. |
| `goldstar passwd` | Generate the dashboard password hash. |
| `goldstar doctor` | Check config and mailbox connectivity. Changes nothing. |
| `goldstar examples` | Register new reference invoices from the examples folder. |
| `goldstar eval` | Re-extract every example and score accuracy. |
| `goldstar models` | List the Gemini models your API key can call. |

## The dashboard

Monochrome, keyboard-friendly, and driven entirely by the buttons — no need to
touch the CLI once it is running.

- **Search bar** — one query across vehicles, parts, suppliers and invoices at
  once, with a date range beside it that narrows every group, not just the
  invoice list. `/` focuses it from anywhere; Enter drops you into the filtered
  invoice list.
- **Sync mailbox** — pulls and extracts new invoices, streaming a live log while
  it works. The request returns immediately; the job runs in the background and
  only one runs at a time.
- **Drag and drop** a PDF or a phone photo of a paper invoice onto the Invoices
  tab to put it through the same extraction.
- **Overview** — spend tiles, a gross-spend-by-month chart, top vehicles.
- **Invoices** — full-text search across registrations, part numbers, suppliers
  and invoice numbers; date-range, supplier, vehicle and review filters;
  sortable columns; pagination. Click any row to open it.
- **Detail drawer** — every extracted field is editable, so a misread figure can
  be corrected by hand. Shows all line items, the mail it came from, the file
  checksum, and a link to the original document.
- **Vehicles** — spend per registration. For a taxi fleet this is the view that
  answers which car is costing money.
- **Parts** — ranked by spend, with how many distinct vehicles each part was
  fitted to. A part recurring on a single vehicle is worth investigating.
- **Spending** — 7 / 30 / 90 / 365-day windows or a custom range, each shown
  against the equally-long window immediately before it so the number means
  something. Filter by registration or by vehicle-work / general-stock. Below
  the chart, a scrollable day-by-day list of every item bought and what it cost.
- **Suppliers** and **VAT** — spend by supplier, and input VAT by month.
- **Exports** — generate a workbook for the last 24 hours, 7 / 30 / 90 days,
  everything, or a custom range. Generated files are kept on disk and listed
  below the buttons with their invoice count, line count and total; preview any
  of them sheet by sheet in the browser before downloading. The listing is
  cached and only rebuilt when the folder actually changes.
- **Vehicle pages** — click any registration for its spend over time, suppliers
  used, every part fitted, and cost per month.
- **Part pages** — unit-price history for a part number, so a supplier quietly
  raising a price shows up as a percentage rather than something you have to
  spot by eye. Also shows which vehicles it went on: the same part recurring on
  one vehicle is a symptom.
- **Fleet** — assign each registration to GOLDSTAR DIAMOND CARS, MFS MOTORGROUP
  or Overall Clients (the default for anything not yet assigned), with make,
  model, driver and on/off-fleet status. Plates seen on invoices but not yet
  registered are listed for triage.
- **Training** — teach the extractor (see below).
- **Admin** — connection tests, configuration, password change, database
  backup and compaction.
- **Excel / CSV** exports honour whatever filters are active, so a download
  matches what is on screen.

Everything is served from the single binary; there is no npm, no build step and
no CDN.

## Teaching it to be more accurate

Extraction is never guaranteed correct, but it improves markedly on repeat
suppliers — which is most of the volume. Three mechanisms, all on the Training
page:

The form asks for two things per field: the **correct value**, and **where on
the page it was found** — "the 'Vehicle Reg' line under Account", "the totals
box bottom right". The values are what accuracy is scored against; the locations
are compiled into a sentence telling the model where to look on that supplier's
invoices, so a single correction keeps paying off. Filling the form writes the
hint for you.

**Example invoices.** Drop real invoices into

```
<install folder>/data/examples/
```

or drag them onto the Training page (same thing — the page writes to that
folder). Open each one, press **Auto-fill with AI** to populate the form, then
correct whatever it got wrong and save. Corrected examples are sent with every
future extraction as worked examples.

**Supplier hints.** Written for you from the location notes above, and editable
by hand. Injected only when that supplier is recognised, so the prompt stays
small however many suppliers accumulate.

**Accuracy checks.** Press **Run accuracy check** (or `goldstar eval`) to
re-extract every completed example and score it field by field against what you
typed. Worked examples are deliberately withheld during evaluation, so an
example cannot grade itself; the location notes are kept, because production
extraction gets those too and withholding them would understate real accuracy. Results are kept as history, which is what makes a
prompt or model change safe: a regression shows up as a number instead of
surfacing months later in a VAT return.

Note that a mismatch may mean your typed values are incomplete rather than the
model being wrong — the check reports the disagreement, not who is at fault.

## Publishing it (Cloudflare tunnel)

`goldstar serve` **refuses to start without a password.** That is deliberate:
`cloudflared` connects to the app over loopback, so binding to `127.0.0.1` is no
evidence that a site is private — a tunnel would publish it to the internet
regardless.

```sh
goldstar passwd     # paste the printed line into your .env
```

Full walkthrough, including Cloudflare Access as a second gate, plus an honest
list of what is *not* protected: **[deploy/TUNNEL.md](deploy/TUNNEL.md)**.

Security built in: Argon2id password hashing, HMAC-signed `HttpOnly` session
cookies, CSRF tokens on every mutating request, login rate limiting keyed on
`CF-Connecting-IP`, a strict CSP, and archived documents served only by database
id from inside the attachments directory.

## Where the data lives

```
<install folder>/data/
├── goldstar.db                          SQLite — the record of truth
├── attachments/2026/08/<hash>-inv.pdf   every original document, kept
├── examples/                            drop reference invoices here
└── exports/goldstar-invoices-<date>.xlsx
```

The spreadsheet is a derived view. Delete it any time and rebuild with
`goldstar export`; the originals and the database are what matter.

## The workbook

Three sheets, frozen headers and filters on each:

- **Invoices** — one row per invoice. Rows needing attention are highlighted.
- **Items** — one row per part line. This is the sheet to filter by part number
  or registration.
- **Summary** — netto/VAT/brutto totalled by calendar month, shaped for a VAT
  return.

Column widths are measured from the actual contents rather than guessed, with a
ceiling so a long note cannot stretch a column off the screen.

## Running it safely twice

Re-running is always safe. Messages are tracked by IMAP UID, and every
attachment by SHA-256 of its bytes, so the same invoice is never counted twice
even if a supplier resends it or someone forwards it on. Duplicate detection
happens *before* the Gemini call, so re-runs cost nothing.

Mail is never deleted. Processed messages get a `Goldstar-Processed` keyword,
visible in any mail client.

## When it flags an invoice

An invoice is marked **needs review** when something genuinely does not add up:

- netto + VAT disagrees with brutto by more than a penny
- the line items do not sum to the invoice netto
- no totals, or an unparseable date
- no vehicle registration anywhere on the document
- the model itself reported low confidence

These end up highlighted in the workbook and counted on the dashboard. The
model's own commentary is recorded as a note but does **not** trip the flag on
its own — it remarks on routine things, and a flag that fires every time would
be worth nothing.

## Privacy

Invoice PDFs are sent to Google's Gemini API for extraction. That is the one
thing that leaves this machine. Everything else — database, original documents,
spreadsheets, dashboard — is local, and the dashboard binds to 127.0.0.1 with
no authentication, so do not expose it to a network.

If that tradeoff stops working for you, `internal/extract` is the only package
that talks to Google; a local model or a rules-based parser would slot in behind
the same `Extract` method.

## Configuration

All settings live in `.env` beside the binary (mode 600). Real
environment variables override the file, so a systemd drop-in or a one-off
shell export always wins. See [.env.example](.env.example).

## Changing the schedule

```sh
systemctl --edit --user goldstar.timer     # change OnCalendar
systemctl --user list-timers goldstar.timer
journalctl --user -u goldstar.service -n 50
```

The timer uses `Persistent=true`, so a day missed with the machine off runs as
soon as it next boots. `install.sh` enables lingering; without it a user timer
would not fire unless someone were logged in.
# Garage
