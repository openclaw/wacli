package main

import (
	"os"
	"runtime"
	"strings"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"google.golang.org/protobuf/proto"
)

func main() {
	applyDeviceLabel()
	if err := execute(os.Args[1:]); err != nil {
		os.Exit(1)
	}
}

func applyDeviceLabel() {
	label := strings.TrimSpace(os.Getenv("WACLI_DEVICE_LABEL"))
	platformRaw := strings.TrimSpace(os.Getenv("WACLI_DEVICE_PLATFORM"))
	if platformRaw == "" {
		platformRaw = "DESKTOP"
	}
	if label == "" {
		label = detectDeviceLabel(runtime.GOOS, os.Hostname, os.ReadFile)
	}
	platform := parsePlatformType(platformRaw)
	store.DeviceProps.PlatformType = platform.Enum()
	if label == "" {
		label = "wacli"
	}

	store.SetOSInfo(label, [3]uint32{0, 1, 0})
	store.BaseClientPayload.UserAgent.Device = proto.String(label)
	store.BaseClientPayload.UserAgent.Manufacturer = proto.String(label)
}

func parsePlatformType(raw string) waCompanionReg.DeviceProps_PlatformType {
	value := strings.TrimSpace(raw)
	if value == "" {
		return waCompanionReg.DeviceProps_CHROME
	}
	value = strings.ToUpper(value)
	if enumValue, ok := waCompanionReg.DeviceProps_PlatformType_value[value]; ok {
		return waCompanionReg.DeviceProps_PlatformType(enumValue)
	}
	return waCompanionReg.DeviceProps_CHROME
}

func detectDeviceLabel(goos string, hostname func() (string, error), readFile func(string) ([]byte, error)) string {
	host, _ := hostname()
	host = strings.TrimSpace(host)
	osName := friendlyOSName(goos, readFile)
	switch {
	case host != "" && osName != "":
		return "wacli - " + osName + " (" + host + ")"
	case host != "":
		return "wacli - " + host
	case osName != "":
		return "wacli - " + osName
	default:
		return "wacli"
	}
}

func friendlyOSName(goos string, readFile func(string) ([]byte, error)) string {
	switch goos {
	case "darwin":
		return "macOS"
	case "linux":
		return linuxDistroName(readFile)
	case "windows":
		return "Windows"
	default:
		return goos
	}
}

func linuxDistroName(readFile func(string) ([]byte, error)) string {
	data, err := readFile("/etc/os-release")
	if err != nil {
		return "Linux"
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key != "PRETTY_NAME" {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		if value != "" {
			return value
		}
	}
	return "Linux"
}
