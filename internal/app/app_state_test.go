package app

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestAppStatePersistenceSequencerPreservesReservationOrder(t *testing.T) {
	var sequencer appStatePersistenceSequencer
	var mu sync.Mutex
	var order []int
	record := func(value int) func() {
		return func() {
			mu.Lock()
			order = append(order, value)
			mu.Unlock()
		}
	}

	ticket := sequencer.reserve()
	sequencer.enqueue(record(2))
	frontier := sequencer.complete(ticket, record(1))
	if err := sequencer.waitThrough(context.Background(), frontier); err != nil {
		t.Fatalf("waitThrough: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(order, []int{1, 2}) {
		t.Fatalf("persistence order = %v, want [1 2]", order)
	}
}

func TestAppStatePersistenceSequencerDoesNotOvertakeLiveEvent(t *testing.T) {
	var sequencer appStatePersistenceSequencer
	var mu sync.Mutex
	var order []int
	liveStarted := make(chan struct{})
	releaseLive := make(chan struct{})
	liveDone := make(chan struct{})
	go func() {
		sequencer.enqueue(func() {
			close(liveStarted)
			<-releaseLive
			mu.Lock()
			order = append(order, 1)
			mu.Unlock()
		})
		close(liveDone)
	}()
	<-liveStarted

	ticket := sequencer.reserve()
	frontier := sequencer.complete(ticket, func() {
		mu.Lock()
		order = append(order, 2)
		mu.Unlock()
	})
	close(releaseLive)
	if err := sequencer.waitThrough(context.Background(), frontier); err != nil {
		t.Fatalf("waitThrough: %v", err)
	}
	<-liveDone
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(order, []int{1, 2}) {
		t.Fatalf("persistence order = %v, want [1 2]", order)
	}
}

func TestAppStatePersistenceSequencerWaitsForFixedFrontier(t *testing.T) {
	var sequencer appStatePersistenceSequencer
	ticket := sequencer.reserve()
	reservedStarted := make(chan struct{})
	releaseReserved := make(chan struct{})
	completeDone := make(chan struct{})
	go func() {
		sequencer.complete(ticket, func() {
			close(reservedStarted)
			<-releaseReserved
		})
		close(completeDone)
	}()
	<-reservedStarted

	laterStarted := make(chan struct{})
	releaseLater := make(chan struct{})
	laterDone := make(chan struct{})
	sequencer.enqueue(func() {
		close(laterStarted)
		<-releaseLater
		close(laterDone)
	})
	close(releaseReserved)
	select {
	case <-completeDone:
	case <-time.After(time.Second):
		t.Fatal("reservation waited for a task beyond its fixed frontier")
	}
	<-laterStarted
	close(releaseLater)
	<-laterDone
}

func TestAppStatePersistenceSequencerSkipsLaterUnreadyReservation(t *testing.T) {
	var sequencer appStatePersistenceSequencer
	first := sequencer.reserve()
	second := sequencer.reserve()
	frontier := sequencer.complete(first, func() {})
	if err := sequencer.waitThrough(context.Background(), frontier); err != nil {
		t.Fatalf("waitThrough first: %v", err)
	}
	if frontier != first {
		t.Fatalf("first frontier = %d, want %d before unready ticket %d", frontier, first, second)
	}
	secondFrontier := sequencer.complete(second, func() {})
	if err := sequencer.waitThrough(context.Background(), secondFrontier); err != nil {
		t.Fatalf("waitThrough second: %v", err)
	}
}
