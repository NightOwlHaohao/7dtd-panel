package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPublishDoesNotBlockOnStalledSubscriber(t *testing.T) {
	b := NewEventBroker()
	b.subscribe()
	done := make(chan struct{})
	go func() {
		for range 600 {
			b.Publish(Event{Type: "test"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on stalled subscriber")
	}
}

func TestEventBrokerServesSSE(t *testing.T) {
	b := NewEventBroker()
	b.Publish(Event{Type: "log", Message: "hello"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rr := httptest.NewRecorder()
	b.ServeHTTP(rr, httptest.NewRequest("GET", "/api/events", nil).WithContext(ctx))
	if !strings.Contains(rr.Body.String(), `"message":"hello"`) {
		t.Fatalf("body=%q", rr.Body.String())
	}
}

func TestEventBrokerCopiesExistingLogPublicationsToConsole(t *testing.T) {
	store, broker := NewConsoleStore(), NewEventBroker()
	broker.SetConsoleStore(store)
	broker.Publish(Event{Type: "log", Source: "server", Message: "game line"})
	broker.Publish(Event{Type: "console", Source: "steamcmd", Message: "steam line"})
	broker.Publish(Event{Type: "console", Source: "telnet", Message: "telnet line"})
	if got := store.Since("game", 0, 1); len(got) != 1 || got[0].Text != "game line" {
		t.Fatalf("game = %#v", got)
	}
	if got := store.Since("steamcmd", 0, 1); len(got) != 1 || got[0].Text != "steam line" {
		t.Fatalf("steam = %#v", got)
	}
	if got := store.Since("telnet", 0, 1); len(got) != 1 || got[0].Level != "output" {
		t.Fatalf("telnet = %#v", got)
	}
}
