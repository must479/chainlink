package utils

import (
	"sync"
	"sync/atomic"
)

// Mailbox contains a notify channel,
// a mutual exclusive lock,
// a queue of interfaces,
// and a queue capacity.
type Mailbox[T any] struct {
	mu       sync.Mutex
	chNotify chan struct{}
	queue    []T
	queueLen atomic.Int64 // atomic so monitor can read w/o blocking the queue

	// capacity - number of items the mailbox can buffer
	// NOTE: if the capacity is 1, it's possible that an empty Retrieve may occur after a notification.
	capacity uint64
	// name of this Mailbox for prometheus monitoring (if non-empty).
	name string
}

// NewHighCapacityMailbox create a new mailbox with a capacity
// that is better able to handle e.g. large log replays
func NewHighCapacityMailbox[T any](name string) *Mailbox[T] {
	return NewMailbox[T](100_000, name)
}

func NewSingleMailbox[T any]() *Mailbox[T] { return NewMailbox[T](1, "") }

// NewMailbox creates a new mailbox instance. If name is non-empty, it must be unique and calling Start will launch
// prometheus metric monitor that periodically reports mailbox load until Close() is called.
func NewMailbox[T any](capacity uint64, name string) *Mailbox[T] {
	queueCap := capacity
	if queueCap == 0 {
		queueCap = 100
	}
	m := &Mailbox[T]{
		chNotify: make(chan struct{}, 1),
		queue:    make([]T, 0, queueCap),
		capacity: capacity,
		name:     name,
	}
	if name != "" {
		monitor(name, m.load)
	}
	return m
}

// Notify returns the contents of the notify channel
func (m *Mailbox[T]) Notify() <-chan struct{} {
	return m.chNotify
}

func (m *Mailbox[T]) Close() error {
	if m.name != "" {
		unMonitor(m.name)
	}
	close(m.chNotify)
	return nil
}

func (m *Mailbox[T]) load() (uint64, float64) {
	pct := 100 * float64(m.queueLen.Load()) / float64(m.capacity)
	return m.capacity, pct
}

// Deliver appends to the queue
func (m *Mailbox[T]) Deliver(x T) (wasOverCapacity bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.queue = append([]T{x}, m.queue...)
	if uint64(len(m.queue)) > m.capacity && m.capacity > 0 {
		m.queue = m.queue[:len(m.queue)-1]
		wasOverCapacity = true
	} else {
		m.queueLen.Add(1)
	}

	select {
	case m.chNotify <- struct{}{}:
	default:
	}
	return
}

// Retrieve fetches from the queue
func (m *Mailbox[T]) Retrieve() (t T, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.queue) == 0 {
		return
	}
	t = m.queue[len(m.queue)-1]
	m.queue = m.queue[:len(m.queue)-1]
	m.queueLen.Add(-1)
	ok = true
	return
}

func (m *Mailbox[T]) RetrieveAll() []T {
	m.mu.Lock()
	defer m.mu.Unlock()
	queue := m.queue
	m.queue = nil
	m.queueLen.Store(0)
	for i, j := 0, len(queue)-1; i < j; i, j = i+1, j-1 {
		queue[i], queue[j] = queue[j], queue[i]
	}
	return queue
}

// RetrieveLatestAndClear returns the latest value (or nil), and clears the queue.
func (m *Mailbox[T]) RetrieveLatestAndClear() (t T) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.queue) == 0 {
		return
	}
	t = m.queue[0]
	m.queue = nil
	m.queueLen.Store(0)
	return
}
