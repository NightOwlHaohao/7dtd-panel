package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTelnetFromConfigAllowsEnabledLoopbackWithoutPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serverconfig.xml")
	if err := os.WriteFile(path, []byte(`<ServerSettings><property name="TelnetEnabled" value="true"/><property name="TelnetPort" value="8081"/><property name="TelnetPassword" value=""/></ServerSettings>`), 0600); err != nil {
		t.Fatal(err)
	}
	client, err := telnetFromConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if client.Address != "127.0.0.1:8081" || client.Password != "" {
		t.Fatalf("client=%+v", client)
	}
}

func TestTelnetFromConfigRejectsDisabledTelnet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "serverconfig.xml")
	if err := os.WriteFile(path, []byte(`<ServerSettings><property name="TelnetEnabled" value="false"/><property name="TelnetPort" value="8081"/><property name="TelnetPassword" value="secret"/></ServerSettings>`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := telnetFromConfig(path); err == nil {
		t.Fatal("disabled Telnet was accepted")
	}
}

func TestTelnetCommandWithoutPasswordSendsCommandAfterImmediatePrompt(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	done := make(chan error, 1)
	go func() {
		_, _ = peer.Write([]byte("Welcome\r\n> "))
		buf := make([]byte, 64)
		n, err := peer.Read(buf)
		if err != nil || string(buf[:n]) != "shutdown\r\n" {
			done <- errors.New("passwordless Telnet sent unexpected bytes before shutdown")
			return
		}
		_, _ = peer.Write([]byte("shutting down\r\n> "))
		done <- nil
	}()
	got, err := (TelnetClient{Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}).Command(context.Background(), "shutdown")
	if err != nil || !strings.Contains(got, "shutting down") {
		t.Fatalf("Command()=(%q,%v)", got, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTelnetShutdownWithoutPasswordWaitsForSessionGreeting(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	done := make(chan error, 1)
	go func() {
		defer peer.Close()
		if err := peer.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
			done <- err
			return
		}
		one := make([]byte, 1)
		if _, err := peer.Read(one); err == nil {
			done <- errors.New("passwordless shutdown wrote before the session greeting")
			return
		} else if netErr, ok := err.(net.Error); !ok || !netErr.Timeout() {
			done <- err
			return
		}
		if err := peer.SetReadDeadline(time.Time{}); err != nil {
			done <- err
			return
		}
		if _, err := peer.Write([]byte("Connected with 7DTD server. Press 'help' for a list of commands.\r\nStarted Telnet session.\r\n")); err != nil {
			done <- err
			return
		}
		buf := make([]byte, len("shutdown\r\n"))
		if _, err := io.ReadFull(peer, buf); err != nil || string(buf) != "shutdown\r\n" {
			done <- errors.New("passwordless shutdown did not write shutdown command")
			return
		}
		done <- peer.Close()
	}()
	if err := (TelnetClient{Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}).Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown()=%v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTelnetShutdownWithoutPasswordUsesImmediatePromptWithoutEmptyLogin(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	done := make(chan error, 1)
	go func() {
		writeDone := make(chan error, 1)
		go func() {
			_, err := peer.Write([]byte("Welcome\r\n> "))
			writeDone <- err
		}()
		select {
		case err := <-writeDone:
			if err != nil {
				done <- err
				return
			}
		case <-time.After(100 * time.Millisecond):
			_ = peer.Close()
			done <- errors.New("shutdown wrote an empty login line before reading the prompt")
			return
		}
		buf := make([]byte, len("shutdown\r\n"))
		if _, err := io.ReadFull(peer, buf); err != nil || string(buf) != "shutdown\r\n" {
			done <- errors.New("shutdown did not write only the shutdown command after prompt")
			return
		}
		done <- peer.Close()
	}()
	if err := (TelnetClient{Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}).Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown()=%v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestTelnetShutdownWaitsForServerResponseBeforeClosing(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	clientDone := make(chan error, 1)
	go func() {
		clientDone <- (TelnetClient{Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}).Shutdown(context.Background())
	}()
	if _, err := peer.Write([]byte("Welcome\r\n> ")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("shutdown\r\n"))
	if _, err := io.ReadFull(peer, buf); err != nil || string(buf) != "shutdown\r\n" {
		t.Fatalf("shutdown command = %q, %v", buf, err)
	}
	select {
	case err := <-clientDone:
		t.Fatalf("Shutdown() returned before the server could respond: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := peer.Write([]byte("shutting down\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := peer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-clientDone; err != nil {
		t.Fatalf("Shutdown()=%v after server close", err)
	}
}

func TestTelnetShutdownCancelsBlockedCommandWrite(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	go func() {
		buf := make([]byte, len("\r\n"))
		_, _ = io.ReadFull(peer, buf)
		_, _ = peer.Write([]byte("Welcome\r\n> "))
		<-time.After(400 * time.Millisecond)
		_ = peer.Close()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-time.After(200 * time.Millisecond)
		cancel()
	}()
	err := (TelnetClient{Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}).Shutdown(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown()=%v, want context cancellation", err)
	}
}

func TestTelnetShutdownCancellationInterruptsBlockedLoginRead(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(20*time.Millisecond, cancel)
	defer timer.Stop()
	started := time.Now()

	err := (TelnetClient{Password: "secret", Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}).Shutdown(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown()=%v, want context cancellation", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("cancellation took %v", elapsed)
	}
}

func TestTelnetShutdownReportsDisconnectBeforeCommandWrite(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	go func() {
		buf := make([]byte, len("\r\n"))
		_, _ = io.ReadFull(peer, buf)
		_ = peer.Close()
	}()
	if err := (TelnetClient{Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}).Shutdown(context.Background()); err == nil {
		t.Fatal("Shutdown() succeeded after disconnect before command write")
	}
}

func TestTelnetCommandNegotiatesAndDoesNotReturnControls(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	c := TelnetClient{Address: "unused", Password: "secret", Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	done := make(chan error, 1)
	go func() {
		defer close(done)
		_, _ = peer.Write([]byte{255, 253, 1, 255, 251, 3, 255, 250, 1, 2, 255, 240, 'P', 'a', 's', 's', 'w', 'o', 'r', 'd', ':'})
		buf := make([]byte, 64)
		n, err := io.ReadFull(peer, buf[:14])
		if err != nil || string(buf[:n]) != "\xff\xfc\x01\xff\xfe\x03secret\r\n" {
			done <- errors.New("wrong negotiation or password")
			return
		}
		_, _ = peer.Write([]byte("Welcome\r\n"))
		n, err = peer.Read(buf)
		if err != nil || string(buf[:n]) != "lp\r\n" {
			done <- errors.New("wrong command")
			return
		}
		_, _ = peer.Write([]byte("Total of 12 in the game\r\n> "))
		done <- nil
	}()
	got, err := c.Command(context.Background(), "lp")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Welcome\r\nTotal of 12 in the game\r\n> " {
		t.Fatalf("output=%q", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestShutdownTimeoutDoesNotTerminate(t *testing.T) {
	started := time.Unix(10, 0)
	probe := &fakeProcessProbe{identity: ProcessIdentity{PID: 42, Exe: `C:\\server.exe`, Started: started}}
	m, paths := testManager(t, probe)
	writeRecordedProcess(t, paths, persistedProcess{PID: 42, Exe: `C:\\server.exe`, Started: started})
	m.shutdownTimeout = 20 * time.Millisecond
	client, peer := net.Pipe()
	defer peer.Close()
	go func() {
		_, _ = peer.Write([]byte("Password:"))
		buf := make([]byte, 64)
		_, _ = peer.Read(buf)
		_, _ = peer.Write([]byte("Welcome"))
		_, _ = peer.Read(buf)
		_, _ = peer.Write([]byte("accepted> "))
	}()
	if err := m.Shutdown(context.Background(), TelnetClient{Password: "secret", Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}); !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("Shutdown()=%v", err)
	}
	if got := m.Status().State; got != ServerStopTimeout {
		t.Fatalf("state=%q", got)
	}
	if probe.terminatedCount() != 0 {
		t.Fatalf("terminated=%d", probe.terminatedCount())
	}
}

func TestParseOnlineCount(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want int
		ok   bool
	}{
		{"Total of 0 in the game", 0, true},
		{"Total of 12 in the game", 12, true},
		{"players: twelve", 0, false},
	} {
		got, ok := ParseOnlineCount(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseOnlineCount(%q)=(%d,%t), want (%d,%t)", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestTelnetCommandCompletesAtPromptWithoutEOF(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = peer.Write([]byte("Password:"))
		buf := make([]byte, 64)
		_, _ = peer.Read(buf)
		_, _ = peer.Write([]byte("Welcome> "))
		_, _ = peer.Read(buf)
		_, _ = peer.Write([]byte("done\r\n> ")) // keep the connection open
		_, _ = peer.Read(buf)
	}()
	c := TelnetClient{Password: "secret", CommandIdle: 20 * time.Millisecond, Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	got, err := c.Command(context.Background(), "lp")
	if err != nil || !strings.Contains(got, "done") {
		t.Fatalf("Command()=(%q,%v)", got, err)
	}
}

func TestTelnetCommandCompletesAfterResponseIdleWithoutEOF(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	go func() {
		_, _ = peer.Write([]byte("Password:"))
		buf := make([]byte, 64)
		_, _ = peer.Read(buf)
		_, _ = peer.Write([]byte("Welcome> "))
		_, _ = peer.Read(buf)
		_, _ = peer.Write([]byte("Total of 12 in the game\r\n"))
		_, _ = peer.Read(buf)
	}()
	c := TelnetClient{Password: "secret", CommandIdle: 20 * time.Millisecond, Dial: func(context.Context, string, string) (net.Conn, error) { return client, nil }}
	got, err := c.Command(context.Background(), "lp")
	if err != nil || !strings.Contains(got, "Total of 12") {
		t.Fatalf("Command()=(%q,%v)", got, err)
	}
}

func TestTelnetParserConsumesSplitControls(t *testing.T) {
	p := telnetParser{}
	var text []byte
	var replies [][]byte
	for _, frame := range [][]byte{{'P', 255}, {253}, {1, 'a', 255, 251}, {3, 's', 255, 250, 1}, {2, 255}, {240, 's', ':'}} {
		out, response := p.feed(frame)
		text, replies = append(text, out...), append(replies, response...)
	}
	if string(text) != "Pass:" {
		t.Fatalf("text=%q", text)
	}
	if string(replies[0]) != "\xff\xfc\x01" || string(replies[1]) != "\xff\xfe\x03" {
		t.Fatalf("replies=%q", replies)
	}
}
