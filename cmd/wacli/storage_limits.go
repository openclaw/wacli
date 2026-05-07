package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	envSyncMaxMessages = "WACLI_SYNC_MAX_MESSAGES"
	envSyncMaxDBSize   = "WACLI_SYNC_MAX_DB_SIZE"
)

type syncStorageLimitFlags struct {
	maxMessages    int64
	maxMessagesSet bool
	maxDBSize      string
}

func resolveSyncStorageLimits(flags syncStorageLimitFlags) (int64, int64, error) {
	maxMessages := flags.maxMessages
	if !flags.maxMessagesSet && maxMessages <= 0 {
		raw := strings.TrimSpace(os.Getenv(envSyncMaxMessages))
		if raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n < 0 {
				return 0, 0, fmt.Errorf("%s must be a non-negative integer", envSyncMaxMessages)
			}
			maxMessages = n
		}
	}

	maxDBSizeRaw := strings.TrimSpace(flags.maxDBSize)
	if maxDBSizeRaw == "" {
		maxDBSizeRaw = strings.TrimSpace(os.Getenv(envSyncMaxDBSize))
	}
	maxDBSize, err := parseByteSize(maxDBSizeRaw)
	if err != nil {
		return 0, 0, err
	}
	return maxMessages, maxDBSize, nil
}

func parseByteSize(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "0" {
		return 0, nil
	}
	s := strings.ToUpper(raw)
	multiplier := int64(1)
	for _, suffix := range []struct {
		s string
		m int64
	}{
		{"KIB", 1024},
		{"KB", 1024},
		{"K", 1024},
		{"MIB", 1024 * 1024},
		{"MB", 1024 * 1024},
		{"M", 1024 * 1024},
		{"GIB", 1024 * 1024 * 1024},
		{"GB", 1024 * 1024 * 1024},
		{"G", 1024 * 1024 * 1024},
		{"B", 1},
	} {
		if strings.HasSuffix(s, suffix.s) {
			multiplier = suffix.m
			s = strings.TrimSpace(strings.TrimSuffix(s, suffix.s))
			break
		}
	}
	value, err := strconv.ParseFloat(s, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid byte size %q", raw)
	}
	return int64(value * float64(multiplier)), nil
}
