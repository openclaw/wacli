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

func TestAppCloseDrainsHandedOffAppStatePersistence(t *testing.T) {
	a := newTestApp(t)
	a.wa = newFakeWA()

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan struct{})
	go func() {
		a.appStatePersist.enqueue(func() {
			close(firstStarted)
			<-releaseFirst
		})
		close(firstDone)
	}()
	<-firstStarted

	secondStarted := make(chan struct{})
	releaseSecond := make(chan struct{})
	writeErr := make(chan error, 1)
	a.appStatePersist.enqueue(func() {
		close(secondStarted)
		<-releaseSecond
		writeErr <- a.db.UpsertChat("123@s.whatsapp.net", "dm", "Alice", time.Now().UTC())
	})

	closeDone := make(chan struct{})
	go func() {
		a.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
		t.Fatal("App.Close returned while the initial persistence task was blocked")
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseFirst)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("handed-off persistence task did not start")
	}
	select {
	case <-closeDone:
		t.Fatal("App.Close returned while the handed-off persistence task was blocked")
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseSecond)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("App.Close did not finish after the persistence queue drained")
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("handed-off persistence write: %v", err)
	}
	<-firstDone
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

func TestAppStatePersistenceSequencerIncludesStartedLiveReservation(t *testing.T) {
	var sequencer appStatePersistenceSequencer
	fetch := sequencer.reserve()
	live := sequencer.reserveLive()
	frontier := sequencer.complete(fetch, func() {})
	if frontier != live {
		t.Fatalf("fetch frontier = %d, want started live ticket %d", frontier, live)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := sequencer.waitThrough(ctx, frontier); err == nil {
		t.Fatal("fetch crossed an uncompleted live reservation")
	}
	sequencer.completeOne(live, func() {})
	if err := sequencer.waitThrough(context.Background(), frontier); err != nil {
		t.Fatalf("waitThrough completed live reservation: %v", err)
	}
}

func TestAppStatePersistenceSequencerDrainsOutOfOrderLiveCompletions(t *testing.T) {
	var sequencer appStatePersistenceSequencer
	var mu sync.Mutex
	var order []int
	first := sequencer.reserveLive()
	second := sequencer.reserveLive()
	sequencer.completeOne(second, func() {
		mu.Lock()
		order = append(order, 2)
		mu.Unlock()
	})
	sequencer.completeOne(first, func() {
		mu.Lock()
		order = append(order, 1)
		mu.Unlock()
	})
	if err := sequencer.waitThrough(context.Background(), second); err != nil {
		t.Fatalf("waitThrough second: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(order, []int{1, 2}) {
		t.Fatalf("persistence order = %v, want [1 2]", order)
	}
}

func TestAppStatePersistenceSequencerDoesNotDrainPastUnreadyLiveTask(t *testing.T) {
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
	fetch := sequencer.reserve()
	firstLive := sequencer.reserveLive()
	secondLive := sequencer.reserveLive()
	sequencer.completeOne(secondLive, record(3))
	frontier := sequencer.complete(fetch, record(1))
	sequencer.completeOne(firstLive, record(2))
	if err := sequencer.waitThrough(context.Background(), frontier); err != nil {
		t.Fatalf("waitThrough: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(order, []int{1, 2, 3}) {
		t.Fatalf("persistence order = %v, want [1 2 3]", order)
	}
}
