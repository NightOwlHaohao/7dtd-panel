package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type Event struct {
	Type    string    `json:"type"`
	Source  string    `json:"source,omitempty"`
	Message string    `json:"message,omitempty"`
	Data    any       `json:"data,omitempty"`
	Time    time.Time `json:"time"`
}

func (b *EventBroker) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	ch, cancel, events := b.subscribe()
	defer cancel()
	for _, event := range events {
		if !writeBrokerEvent(w, event) {
			return
		}
	}
	flusher.Flush()
	for {
		select {
		case <-req.Context().Done():
			return
		case event := <-ch:
			if !writeBrokerEvent(w, event) {
				return
			}
			flusher.Flush()
		}
	}
}

func writeBrokerEvent(w http.ResponseWriter, event Event) bool {
	data, err := json.Marshal(event)
	if err != nil {
		return false
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err == nil
}

type EventBroker struct {
	mu      sync.Mutex
	events  []Event
	subs    map[chan Event]struct{}
	console *ConsoleStore
}

func NewEventBroker() *EventBroker {
	return &EventBroker{subs: make(map[chan Event]struct{})}
}

func (b *EventBroker) SetConsoleStore(store *ConsoleStore) {
	b.mu.Lock()
	b.console = store
	b.mu.Unlock()
}

func (b *EventBroker) Publish(event Event) {
	if event.Time.IsZero() {
		event.Time = time.Now()
	}
	if event.Message != "" {
		event.Message = sanitizeConsoleText(event.Message)
	}
	b.mu.Lock()
	if b.console != nil && event.Message != "" {
		channel, level, kind := "panel", "info", event.Type
		switch event.Source {
		case "server":
			channel = "game"
		case "steamcmd", "telnet":
			channel = event.Source
		case "backup", "save":
			channel = "ops"
		}
		if channel == "telnet" && event.Type == "console" {
			level, kind = "output", "output"
		}
		b.console.Append(channel, level, kind, event.Message)
	}
	if len(b.events) == 500 {
		copy(b.events, b.events[1:])
		b.events = b.events[:499]
	}
	b.events = append(b.events, event)
	for ch := range b.subs {
		select {
		case ch <- event:
		default:
		}
	}
	b.mu.Unlock()
}

func (b *EventBroker) subscribe() (<-chan Event, func(), []Event) {
	b.mu.Lock()
	ch := make(chan Event, 500)
	b.subs[ch] = struct{}{}
	events := append([]Event(nil), b.events...)
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}, events
}
