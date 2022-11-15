package utils_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/smartcontractkit/chainlink/core/utils"
)

func TestMailbox(t *testing.T) {
	var (
		expected  = []int{2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
		toDeliver = []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	)

	const capacity = 10
	m := utils.NewMailbox[int](capacity, t.Name())

	// Queue deliveries
	for i, d := range toDeliver {
		atCapacity := m.Deliver(d)
		if atCapacity && i < capacity {
			t.Errorf("mailbox at capacity %d", i)
		} else if !atCapacity && i >= capacity {
			t.Errorf("mailbox below capacity %d", i)
		}
	}

	// Retrieve them
	var recvd []int
	chDone := make(chan struct{})
	go func() {
		defer close(chDone)
		for range m.Notify() {
			for {
				x, exists := m.Retrieve()
				if !exists {
					break
				}
				recvd = append(recvd, x)
			}
		}
	}()

	m.Close()
	<-chDone

	require.Equal(t, expected, recvd)
}

func TestMailbox_RetrieveAll(t *testing.T) {
	var (
		expected  = []int{2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
		toDeliver = []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	)

	const capacity = 10
	m := utils.NewMailbox[int](capacity, t.Name())

	// Queue deliveries
	for i, d := range toDeliver {
		atCapacity := m.Deliver(d)
		if atCapacity && i < capacity {
			t.Errorf("mailbox at capacity %d", i)
		} else if !atCapacity && i >= capacity {
			t.Errorf("mailbox below capacity %d", i)
		}
	}

	require.Equal(t, expected, m.RetrieveAll())
}

func TestMailbox_NoEmptyReceivesWhenCapacityIsTwo(t *testing.T) {
	m := utils.NewMailbox[int](2, t.Name())

	var (
		recvd         []int
		emptyReceives []int
	)

	chDone := make(chan struct{})
	go func() {
		defer close(chDone)
		for range m.Notify() {
			x, exists := m.Retrieve()
			if !exists {
				emptyReceives = append(emptyReceives, recvd[len(recvd)-1])
			} else {
				recvd = append(recvd, x)
			}
		}
	}()

	for i := 0; i < 100000; i++ {
		m.Deliver(i)
	}
	m.Close()

	<-chDone
	require.Len(t, emptyReceives, 0)
}
