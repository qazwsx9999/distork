package main

import "sync"

type eventBroker struct {
	mu      sync.RWMutex
	clients map[int]chan chatMessage
	nextID  int
}

func newEventBroker() *eventBroker {
	return &eventBroker{
		clients: make(map[int]chan chatMessage),
	}
}

func (b *eventBroker) subscribe() (int, <-chan chatMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID
	b.nextID++

	ch := make(chan chatMessage, 32)
	b.clients[id] = ch

	return id, ch
}

func (b *eventBroker) unsubscribe(id int) {
	b.mu.Lock()
	ch, ok := b.clients[id]
	if ok {
		delete(b.clients, id)
		close(ch)
	}
	b.mu.Unlock()
}

func (b *eventBroker) publish(msg chatMessage) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, ch := range b.clients {
		select {
		case ch <- msg:
		default:
		}
	}
}
