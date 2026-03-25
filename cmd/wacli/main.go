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

	// Auto-detect platform if not explicitly set.
	if platformRaw == "" {
		platformRaw = detectPlatform()
	}
	if platformRaw != "" {
		platform := parsePlatformType(platformRaw)
		store.DeviceProps.PlatformType = platform.Enum()
	}

	// Auto-detect device label if not explicitly set.
	if label == "" {
		label = detectDeviceLabel()
	}
	if label == "" {
		return
	}

	store.SetOSInfo(label, [3]uint32{0, 1, 0})
	store.BaseClientPayload.UserAgent.Device = proto.String(label)
	store.BaseClientPayload.UserAgent.Manufacturer = proto.String(label)
}

// detectPlatform returns a platform type string based on runtime.GOOS.
func detectPlatform() string {
	switch runtime.GOOS {
	case "darwin":
		return "DESKTOP"
	case "linux":
		return "DESKTOP"
	case "windows":
		return "DESKTOP"
	default:
		return "DESKTOP"
	}
}

// detectDeviceLabel builds a human-readable label like "wacli · MacBook" or
// "wacli · arch-hostname" so the device is identifiable in WhatsApp's linked
// devices list instead of showing as generic "Outro Dispositivo".
func detectDeviceLabel() string {
	hostname, _ := os.Hostname()
	hostname = strings.TrimSpace(hostname)

	osName := friendlyOSName()

	switch {
	case hostname != "" && osName != "":
		return "wacli · " + osName + " (" + hostname + ")"
	case hostname != "":
		return "wacli · " + hostname
	case osName != "":
		return "wacli · " + osName
	default:
		return "wacli"
	}
}

// friendlyOSName returns a short human-readable OS name.
func friendlyOSName() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "linux":
		return detectLinuxDistro()
	case "windows":
		return "Windows"
	default:
		return runtime.GOOS
	}
}

// detectLinuxDistro reads /etc/os-release to find the distro pretty name.
func detectLinuxDistro() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return "Linux"
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			val := strings.TrimPrefix(line, "PRETTY_NAME=")
			val = strings.Trim(val, "\"")
			if val != "" {
				return val
			}
		}
	}
	return "Linux"
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
