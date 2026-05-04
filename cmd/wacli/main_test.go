package main

import (
	"errors"
	"testing"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
)

func TestParsePlatformType(t *testing.T) {
	if got := parsePlatformType("desktop"); got != waCompanionReg.DeviceProps_DESKTOP {
		t.Fatalf("desktop parsed as %v", got)
	}
	if got := parsePlatformType("bogus"); got != waCompanionReg.DeviceProps_CHROME {
		t.Fatalf("bogus parsed as %v", got)
	}
}

func TestDetectDeviceLabel(t *testing.T) {
	host := func() (string, error) { return "workstation", nil }
	readFile := func(string) ([]byte, error) { return []byte(`PRETTY_NAME="Ubuntu 24.04 LTS"`), nil }

	if got := detectDeviceLabel("linux", host, readFile); got != "wacli - Ubuntu 24.04 LTS (workstation)" {
		t.Fatalf("detectDeviceLabel = %q", got)
	}
}

func TestDetectDeviceLabelFallbacks(t *testing.T) {
	noHost := func() (string, error) { return "", errors.New("no hostname") }
	noFile := func(string) ([]byte, error) { return nil, errors.New("missing") }

	if got := detectDeviceLabel("darwin", noHost, noFile); got != "wacli - macOS" {
		t.Fatalf("darwin label = %q", got)
	}
	if got := detectDeviceLabel("", noHost, noFile); got != "wacli" {
		t.Fatalf("empty label = %q", got)
	}
}
