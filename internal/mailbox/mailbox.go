// Package mailbox pulls invoice attachments off an IMAP mailbox (Hostinger by
// default). It never deletes mail — processed messages are only flagged.
package mailbox

import (
	"crypto/tls"
	"fmt"
	"io"
	"mime"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gomessage "github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
)

// Attachment is one candidate invoice file found on a message.
type Attachment struct {
	Filename string
	MIMEType string
	Data     []byte
}

// Message is an email carrying at least one candidate attachment.
type Message struct {
	UID         int64
	Subject     string
	From        string
	Date        time.Time
	Attachments []Attachment
}

type Options struct {
	Host, User, Pass, Mailbox string
	Port                      int
	LookbackDays              int
}

type Client struct {
	c       *imapclient.Client
	conn    net.Conn
	mailbox string
}

// extend pushes the I/O deadline out again. Called as each message is
// processed so a long but healthy sync is not cut off mid-download, while a
// stalled one still dies rather than hanging forever.
func (m *Client) extend() {
	if m.conn != nil {
		m.conn.SetDeadline(time.Now().Add(ioTimeout))
	}
}

// dialTimeout bounds how long we wait to establish the TLS connection, and
// ioTimeout bounds each subsequent read or write.
//
// Without these a mailbox that accepts the TCP connection but never completes
// the handshake — a wedged server, a silently dropping firewall — hangs the
// sync forever. Only one job runs at a time, so a single hung sync would block
// every later one and invoices would stop arriving with nothing in the log to
// say why.
const (
	dialTimeout = 30 * time.Second
	ioTimeout   = 5 * time.Minute
)

func Connect(o Options) (*Client, error) {
	// JoinHostPort rather than Sprintf: an IPv6 literal needs brackets.
	addr := net.JoinHostPort(o.Host, strconv.Itoa(o.Port))

	conn, err := (&net.Dialer{Timeout: dialTimeout}).Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", addr, err)
	}
	// A deadline on the raw connection covers the TLS handshake and the login
	// exchange, which is where a stalled server actually hangs.
	if err := conn.SetDeadline(time.Now().Add(dialTimeout)); err != nil {
		conn.Close()
		return nil, err
	}

	tlsConn := tls.Client(conn, &tls.Config{ServerName: o.Host})
	if err := tlsConn.Handshake(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("tls handshake with %s: %w", addr, err)
	}

	c := imapclient.New(tlsConn, nil)
	if err := c.Login(o.User, o.Pass).Wait(); err != nil {
		c.Close()
		return nil, fmt.Errorf("login as %s: %w", o.User, err)
	}
	if _, err := c.Select(o.Mailbox, nil).Wait(); err != nil {
		c.Close()
		return nil, fmt.Errorf("select mailbox %q: %w", o.Mailbox, err)
	}

	// Downloading attachments takes longer than logging in, so the deadline is
	// relaxed once connected — but never removed.
	if err := conn.SetDeadline(time.Now().Add(ioTimeout)); err != nil {
		c.Close()
		return nil, err
	}
	return &Client{c: c, conn: conn, mailbox: o.Mailbox}, nil
}

func (m *Client) Close() error {
	m.c.Logout().Wait()
	return m.c.Close()
}

// SearchRecent returns UIDs of messages received within the lookback window.
func (m *Client) SearchRecent(lookbackDays int) ([]int64, error) {
	criteria := &imap.SearchCriteria{
		Since: time.Now().AddDate(0, 0, -lookbackDays),
	}
	data, err := m.c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	uids := data.AllUIDs()
	out := make([]int64, 0, len(uids))
	for _, u := range uids {
		out = append(out, int64(u))
	}
	return out, nil
}

// FetchMessage downloads one message and returns it with any attachment that
// looks like an invoice document.
func (m *Client) FetchMessage(uid int64) (*Message, error) {
	// Each message gets a fresh window, so a long mailbox is fine while a
	// stalled transfer still trips the deadline.
	m.extend()
	set := imap.UIDSetNum(imap.UID(uid))
	opts := &imap.FetchOptions{
		UID:         true,
		Envelope:    true,
		BodySection: []*imap.FetchItemBodySection{{}}, // empty section = whole message
	}

	buffers, err := m.c.Fetch(set, opts).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch uid %d: %w", uid, err)
	}
	if len(buffers) == 0 {
		return nil, fmt.Errorf("uid %d not found", uid)
	}
	buf := buffers[0]

	msg := &Message{UID: uid}
	if buf.Envelope != nil {
		msg.Subject = buf.Envelope.Subject
		msg.Date = buf.Envelope.Date
		if len(buf.Envelope.From) > 0 {
			a := buf.Envelope.From[0]
			msg.From = a.Mailbox + "@" + a.Host
		}
	}

	var raw []byte
	for _, bs := range buf.BodySection {
		if len(bs.Bytes) > 0 {
			raw = bs.Bytes
			break
		}
	}
	if raw == nil {
		return msg, nil
	}

	atts, err := parseAttachments(raw)
	if err != nil {
		return msg, fmt.Errorf("parse uid %d: %w", uid, err)
	}
	msg.Attachments = atts
	return msg, nil
}

// Flag applies an IMAP keyword to a message so a human can see in their mail
// client which invoices the tool has already taken.
func (m *Client) Flag(uid int64, keyword string) error {
	set := imap.UIDSetNum(imap.UID(uid))
	store := &imap.StoreFlags{
		Op:     imap.StoreFlagsAdd,
		Silent: true,
		Flags:  []imap.Flag{imap.Flag(keyword)},
	}
	cmd := m.c.Store(set, store, nil)
	if _, err := cmd.Collect(); err != nil {
		return fmt.Errorf("flag uid %d: %w", uid, err)
	}
	return nil
}

// parseAttachments walks the MIME tree and keeps PDFs and images.
func parseAttachments(raw []byte) ([]Attachment, error) {
	mr, err := mail.CreateReader(strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	defer mr.Close()

	var out []Attachment
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			// A single malformed part should not lose the rest of the message.
			if gomessage.IsUnknownCharset(err) || gomessage.IsUnknownEncoding(err) {
				continue
			}
			return out, err
		}

		h, ok := part.Header.(*mail.AttachmentHeader)
		if !ok {
			continue
		}
		filename, _ := h.Filename()
		ct, _, _ := h.ContentType()

		mimeType := detectMIME(filename, ct)
		if mimeType == "" {
			continue
		}
		data, err := io.ReadAll(part.Body)
		if err != nil {
			return out, fmt.Errorf("read attachment %q: %w", filename, err)
		}
		if len(data) == 0 {
			continue
		}
		if filename == "" {
			exts, _ := mime.ExtensionsByType(mimeType)
			ext := ".bin"
			if len(exts) > 0 {
				ext = exts[0]
			}
			filename = "attachment" + ext
		}
		out = append(out, Attachment{Filename: filename, MIMEType: mimeType, Data: data})
	}
	return out, nil
}

// detectMIME returns a Gemini-acceptable type, or "" to skip the attachment.
// It trusts the file extension over the declared type, because mail clients
// frequently send PDFs as application/octet-stream.
func detectMIME(filename, declared string) string {
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".heic":
		return "image/heic"
	}
	switch strings.ToLower(declared) {
	case "application/pdf", "image/png", "image/jpeg", "image/webp", "image/heic":
		return strings.ToLower(declared)
	}
	return ""
}
