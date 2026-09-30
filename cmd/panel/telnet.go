package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var onlineCount = regexp.MustCompile(`(?m)^Total of ([0-9]+) in the game\r?$`)

func ParseOnlineCount(lpOutput string) (int, bool) {
	match := onlineCount.FindStringSubmatch(lpOutput)
	if match == nil {
		return 0, false
	}
	n, err := strconv.Atoi(match[1])
	return n, err == nil
}

const (
	iac  = 255
	do   = 253
	dont = 254
	will = 251
	wont = 252
	sb   = 250
	se   = 240
)

// TelnetClient only implements the small negotiation subset used by the server.
type TelnetClient struct {
	Address     string
	Password    string
	Dial        func(context.Context, string, string) (net.Conn, error)
	CommandIdle time.Duration
}

func telnetFromConfig(path string) (TelnetClient, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return TelnetClient{}, err
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	values := map[string]string{}
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return TelnetClient{}, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "property" {
			continue
		}
		name, value := attrValue(start.Attr, "name"), attrValue(start.Attr, "value")
		values[name] = value
	}
	port, err := strconv.Atoi(values["TelnetPort"])
	if !strings.EqualFold(values["TelnetEnabled"], "true") || err != nil || port < 1 || port > 65535 {
		return TelnetClient{}, errors.New("telnet configuration is unavailable")
	}
	return TelnetClient{Address: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Password: values["TelnetPassword"]}, nil
}

func (c TelnetClient) Command(ctx context.Context, command string) (string, error) {
	conn, p, login, err := c.login(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if err := writeTelnet(ctx, conn, command+"\r\n"); err != nil {
		return "", err
	}
	output, err := c.readUntil(ctx, conn, p, 10*time.Second, func(s string) bool { return strings.HasSuffix(s, "> ") }, c.idle())
	return login + output, err
}

func (c TelnetClient) Shutdown(ctx context.Context) error {
	conn, p, _, err := c.login(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := writeTelnet(ctx, conn, "shutdown\r\n"); err != nil {
		return err
	}
	// The server closes the connection as it shuts down; EOF is success.
	_, err = c.readUntil(ctx, conn, p, 10*time.Second, func(string) bool { return false }, c.idle())
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
		return nil
	}
	return err
}

// login connects and completes the optional password prompt, returning the
// open connection and everything the server printed while logging in.
func (c TelnetClient) login(ctx context.Context) (net.Conn, *telnetParser, string, error) {
	conn, err := c.open(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	p := &telnetParser{}
	fail := func(err error) (net.Conn, *telnetParser, string, error) {
		conn.Close()
		return nil, nil, "", err
	}
	login, err := c.readUntil(ctx, conn, p, 8*time.Second, func(s string) bool {
		return strings.Contains(strings.ToLower(s), "password") || loginDone(s)
	}, 0)
	if err != nil {
		return fail(err)
	}
	if !loginDone(login) {
		if c.Password == "" {
			return fail(errors.New("telnet requested a password but none is configured"))
		}
		if err := writeTelnet(ctx, conn, c.Password+"\r\n"); err != nil {
			return fail(err)
		}
		if login, err = c.readUntil(ctx, conn, p, 8*time.Second, loginDone, 0); err != nil {
			return fail(err)
		}
	}
	if loginFailed(login) {
		return fail(errors.New("telnet login failed"))
	}
	return conn, p, login, nil
}

func (c TelnetClient) idle() time.Duration {
	if c.CommandIdle == 0 {
		return 150 * time.Millisecond
	}
	return c.CommandIdle
}

func (c TelnetClient) open(ctx context.Context) (net.Conn, error) {
	dial := c.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	}
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := dial(dialCtx, "tcp", c.Address)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func loginDone(s string) bool {
	lower := strings.ToLower(s)
	return loginFailed(lower) || strings.Contains(lower, "welcome") || strings.Contains(lower, "session.") || strings.HasSuffix(s, "> ")
}
func loginFailed(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "incorrect") || strings.Contains(s, "failed") || strings.Contains(s, "denied")
}

func (c TelnetClient) readUntil(ctx context.Context, conn net.Conn, p *telnetParser, timeout time.Duration, done func(string) bool, idle time.Duration) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stop()
	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	var text []byte
	buf := make([]byte, 512)
	for {
		readDeadline := deadline
		if len(text) > 0 && idle > 0 && time.Now().Add(idle).Before(readDeadline) {
			readDeadline = time.Now().Add(idle)
		}
		if err := conn.SetReadDeadline(readDeadline); err != nil {
			return string(text), err
		}
		n, err := conn.Read(buf)
		if n > 0 {
			clean, replies := p.feed(buf[:n])
			for _, reply := range replies {
				if err := writeTelnet(ctx, conn, string(reply)); err != nil {
					return "", err
				}
			}
			text = append(text, clean...)
			if done(string(text)) {
				return string(text), nil
			}
		}
		if err != nil {
			if errors.Is(err, net.ErrClosed) || errors.Is(err, context.DeadlineExceeded) {
				return string(text), err
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if len(text) > 0 && idle > 0 && time.Now().Before(deadline) {
					return string(text), nil
				}
				if err := ctx.Err(); err != nil {
					return string(text), err
				}
				return string(text), context.DeadlineExceeded
			}
			if errors.Is(err, io.EOF) || (errors.Is(err, io.ErrClosedPipe) && len(text) > 0) {
				return string(text), nil
			}
			return string(text), err
		}
	}
}

func writeTelnet(ctx context.Context, conn net.Conn, s string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetWriteDeadline(deadline); err != nil {
			return err
		}
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetWriteDeadline(time.Now()) })
	defer func() {
		stop()
		_ = conn.SetWriteDeadline(time.Time{})
	}()
	n, err := conn.Write([]byte(s))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
		return err
	}
	if n != len(s) {
		return io.ErrShortWrite
	}
	return nil
}

type telnetParser struct{ state, verb byte }

const (
	telnetTextState = iota
	telnetIACState
	telnetOptionState
	telnetSubnegotiationState
	telnetSubnegotiationIACState
)

func (p *telnetParser) feed(in []byte) ([]byte, [][]byte) {
	out := make([]byte, 0, len(in))
	var replies [][]byte
	for _, b := range in {
		switch p.state {
		case telnetTextState:
			if b == iac {
				p.state = telnetIACState
			} else {
				out = append(out, b)
			}
		case telnetIACState:
			switch b {
			case iac:
				out, p.state = append(out, iac), telnetTextState
			case do, dont, will, wont:
				p.verb, p.state = b, telnetOptionState
			case sb:
				p.state = telnetSubnegotiationState
			default:
				p.state = telnetTextState
			}
		case telnetOptionState:
			if p.verb == do {
				replies = append(replies, []byte{iac, wont, b})
			}
			if p.verb == will {
				replies = append(replies, []byte{iac, dont, b})
			}
			p.state = telnetTextState
		case telnetSubnegotiationState:
			if b == iac {
				p.state = telnetSubnegotiationIACState
			}
		case telnetSubnegotiationIACState:
			if b == se {
				p.state = telnetTextState
			} else if b != iac {
				p.state = telnetSubnegotiationState
			}
		}
	}
	return out, replies
}
