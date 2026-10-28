package wa

import "testing"

// A refused message is only recoverable when whatsmeow can replay its
// plaintext on redelivery, so the real client must be built with the buffer.
func TestNewClientEnablesReplayOfRefusedEvents(t *testing.T) {
	c, err := New(Options{StorePath: newPairedSessionStore(t), KeyStateStore: newTestKeyStateStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if !c.client.EnableDecryptedEventBuffer {
		t.Fatal("without the decrypted-event buffer a refused message is dropped on redelivery (ErrOldCounter)")
	}
	if !c.client.AutomaticMessageRerequestFromPhone {
		t.Fatal("undecryptable messages are not re-requested from the phone")
	}
}
