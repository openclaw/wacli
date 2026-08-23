package main

import (
	"testing"

	"github.com/openclaw/wacli/internal/store"
)

func TestResolveOptimizedPolicyPresetAndDisable(t *testing.T) {
	p, changed, err := resolveOptimizedPolicy(store.SyncOptimizationPolicy{}, optimizedFlags{enabled: true, changed: true, optimizedSet: true})
	if err != nil || !changed || !p.Enabled || p.MaxChats != 100 || p.MaxMessagesPerChat != 50 {
		t.Fatalf("preset = %+v, changed=%t, err=%v", p, changed, err)
	}
	p, changed, err = resolveOptimizedPolicy(p, optimizedFlags{changed: true, optimizedSet: true})
	if err != nil || !changed || p.Enabled {
		t.Fatalf("disabled policy = %+v, changed=%t, err=%v", p, changed, err)
	}
}

func TestResolveOptimizedPolicyTuningImpliesEnabled(t *testing.T) {
	p, changed, err := resolveOptimizedPolicy(store.SyncOptimizationPolicy{}, optimizedFlags{changed: true, tuning: true, maxChats: 25, excludeArchived: false, pruneEvicted: true})
	if err != nil || !changed || !p.Enabled || p.MaxChats != 25 || p.ExcludeArchived {
		t.Fatalf("policy = %+v, changed=%t, err=%v", p, changed, err)
	}
}
