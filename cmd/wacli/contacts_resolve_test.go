package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/openclaw/wacli/internal/lock"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow/types"
)

type fakeIdentityResolver struct {
	lidToPN map[types.JID]types.JID
	names   map[types.JID]string
}

func (f fakeIdentityResolver) ResolveLIDToPN(_ context.Context, jid types.JID) types.JID {
	if pn, ok := f.lidToPN[jid]; ok {
		return pn
	}
	return jid
}

func (f fakeIdentityResolver) ResolvePNToLID(_ context.Context, jid types.JID) types.JID {
	for lid, pn := range f.lidToPN {
		if pn == jid {
			return lid
		}
	}
	return jid
}

func (f fakeIdentityResolver) ResolveChatName(_ context.Context, jid types.JID, _ string) string {
	if name, ok := f.names[jid]; ok {
		return name
	}
	return jid.User
}

func TestResolveContactIdentity(t *testing.T) {
	pn := types.NewJID("15550000001", types.DefaultUserServer)
	lid := types.NewJID("100000000001", types.HiddenUserServer)
	unnamedPN := types.NewJID("15550000002", types.DefaultUserServer)
	unnamedLID := types.NewJID("100000000002", types.HiddenUserServer)
	resolver := fakeIdentityResolver{
		lidToPN: map[types.JID]types.JID{lid: pn, unnamedLID: unnamedPN},
		names:   map[types.JID]string{pn: "Test Contact"},
	}
	for _, tc := range []struct {
		name  string
		input string
		want  contactResolution
	}{
		{"mapped LID", "100000000001@lid", contactResolution{JID: pn.String(), Phone: pn.User, LID: lid.String(), Name: "Test Contact", Resolved: true}},
		{"LID with device suffix", "100000000001:5@lid", contactResolution{JID: pn.String(), Phone: pn.User, LID: lid.String(), Name: "Test Contact", Resolved: true}},
		{"mapped phone JID", "15550000001@s.whatsapp.net", contactResolution{JID: pn.String(), Phone: pn.User, LID: lid.String(), Name: "Test Contact", Resolved: true}},
		{"formatted phone", "+1 (555) 000-0001", contactResolution{JID: pn.String(), Phone: pn.User, LID: lid.String(), Name: "Test Contact", Resolved: true}},
		{"no name falls back to empty", "100000000002@lid", contactResolution{JID: unnamedPN.String(), Phone: unnamedPN.User, LID: unnamedLID.String(), Resolved: true}},
		{"unknown LID", "100000000009@lid", contactResolution{LID: "100000000009@lid"}},
		{"unknown phone", "15550000009", contactResolution{JID: "15550000009@s.whatsapp.net", Phone: "15550000009"}},
		{"group", "123456789@g.us", contactResolution{Error: "123456789@g.us is not a user JID; groups and channels have no phone/LID pair"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveContactIdentity(context.Background(), resolver, tc.input)
			tc.want.Input = tc.input
			if got != tc.want {
				t.Fatalf("resolve(%q) = %+v, want %+v", tc.input, got, tc.want)
			}
		})
	}
}

func TestResolveContactIdentityWithoutSession(t *testing.T) {
	got := resolveContactIdentity(context.Background(), nil, "100000000001@lid")
	if got.Resolved || got.LID != "100000000001@lid" || got.JID != "" {
		t.Fatalf("without a session store = %+v, want an unresolved LID", got)
	}
}

func TestResolveContactIdentityRejectsGarbage(t *testing.T) {
	got := resolveContactIdentity(context.Background(), fakeIdentityResolver{}, "not a number")
	if got.Resolved || got.Error == "" {
		t.Fatalf("garbage input = %+v, want an error entry", got)
	}
}

func TestWriteContactResolutionsJSONKeepsUnresolved(t *testing.T) {
	results := []contactResolution{
		{Input: "100000000001@lid", JID: "15550000001@s.whatsapp.net", Phone: "15550000001", LID: "100000000001@lid", Resolved: true},
		{Input: "100000000009@lid", LID: "100000000009@lid"},
	}
	var buf bytes.Buffer
	if err := writeContactResolutions(&buf, true, false, results); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON %s: %v", buf.String(), err)
	}
	if len(env.Data) != 2 || env.Data[0]["resolved"] != true || env.Data[1]["resolved"] != false {
		t.Fatalf("data = %v, want both entries with explicit resolved flags", env.Data)
	}
	if _, ok := env.Data[1]["jid"]; ok {
		t.Fatalf("unresolved LID must not invent a jid: %v", env.Data[1])
	}
}

func TestWriteContactResolutionsTable(t *testing.T) {
	var buf bytes.Buffer
	results := []contactResolution{{Input: "123@g.us", Error: "not a user JID"}}
	if err := writeContactResolutions(&buf, false, false, results); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "INPUT") || !strings.Contains(buf.String(), "error: not a user JID") {
		t.Fatalf("table = %q", buf.String())
	}
}

func TestContactsCommandExposesResolve(t *testing.T) {
	cmd := newContactsCmd(&rootFlags{})
	found, _, err := cmd.Find([]string{"resolve"})
	if err != nil || found.Name() != "resolve" {
		t.Fatalf("contacts resolve not registered: %v", err)
	}
}

func TestContactsResolveReadsSyntheticStoreWhileLocked(t *testing.T) {
	dir := seedContactReadStore(t, true)
	lk, err := lock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	sessionBefore, err := os.ReadFile(filepath.Join(dir, "session.db"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := newContactsResolveCmd(&rootFlags{storeDir: dir, asJSON: true})
	cmd.SetArgs([]string{contactLID, contactPN, "999999999@lid"})
	raw := captureRootStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	var result struct{ Data []contactResolution }
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 3 || !result.Data[0].Resolved || !result.Data[1].Resolved || result.Data[2].Resolved {
		t.Fatalf("unexpected resolutions: %s", raw)
	}
	if result.Data[0].Name != "" || result.Data[1].Name != "" {
		t.Fatalf("invented names: %s", raw)
	}
	sessionAfter, err := os.ReadFile(filepath.Join(dir, "session.db"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sessionBefore, sessionAfter) {
		t.Fatal("session was modified")
	}
}

func TestContactsResolveDoesNotCreateStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	cmd := newContactsResolveCmd(&rootFlags{storeDir: dir, asJSON: true})
	cmd.SetArgs([]string{"999999999@lid"})
	_ = cmd.Execute()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("read command created store: %v", err)
	}
}

func TestResolveContactIdentityRejectsMalformedUserJIDs(t *testing.T) {
	for _, raw := range []string{"@lid", "@s.whatsapp.net", "abc@lid", "abc@s.whatsapp.net", "123@lid@lid"} {
		got := resolveContactIdentity(context.Background(), nil, raw)
		if got.Error == "" || got.Resolved {
			t.Errorf("resolve(%q) = %+v", raw, got)
		}
	}
}
