package main

import (
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

const maxReleaseStringLength = 4096

const loadCmdDyldChainedFixups = 0x80000034

type section struct {
	address uint64
	data    []byte
}

type executable struct {
	byteOrder binary.ByteOrder
	symbols   map[string]uint64
	sections  []section
}

type releaseVersion struct {
	Version              string `json:"version"`
	ReleaseLinkerSetting string `json:"releaseLinkerSetting"`
}

func main() {
	if len(os.Args) != 2 {
		fatalf("usage: release-version-inspector <binary>")
	}

	file, err := openExecutable(os.Args[1])
	if err != nil {
		fatalf("inspect %s: %v", os.Args[1], err)
	}
	result := releaseVersion{}
	if result.Version, err = file.goString("main.version"); err != nil {
		fatalf("inspect main.version: %v", err)
	}
	if result.ReleaseLinkerSetting, err = file.goString("main.releaseLinkerSetting"); err != nil {
		fatalf("inspect main.releaseLinkerSetting: %v", err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fatalf("encode result: %v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func openExecutable(name string) (*executable, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var magic [4]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil {
		return nil, err
	}
	switch {
	case magic[0] == 0x7f && string(magic[1:]) == "ELF":
		return openELF(name)
	case string(magic[:2]) == "MZ":
		return openPE(name)
	case isMachOMagic(magic):
		return openMachO(name)
	default:
		return nil, errors.New("unsupported executable format")
	}
}

func isMachOMagic(magic [4]byte) bool {
	value := binary.BigEndian.Uint32(magic[:])
	switch value {
	case macho.Magic32, macho.Magic64, macho.MagicFat,
		0xcefaedfe, 0xcffaedfe:
		return true
	default:
		return false
	}
}

func openELF(name string) (*executable, error) {
	file, err := elf.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	result := &executable{byteOrder: file.ByteOrder, symbols: make(map[string]uint64)}
	symbols, err := file.Symbols()
	if err != nil {
		return nil, fmt.Errorf("read ELF symbols: %w", err)
	}
	for _, symbol := range symbols {
		if err := result.addSymbol(symbol.Name, symbol.Value); err != nil {
			return nil, err
		}
	}
	for _, source := range file.Sections {
		if source.Type == elf.SHT_NOBITS || source.Size == 0 {
			continue
		}
		data, err := source.Data()
		if err != nil {
			return nil, fmt.Errorf("read ELF section %s: %w", source.Name, err)
		}
		result.sections = append(result.sections, section{address: source.Addr, data: data})
	}
	return result, nil
}

func openMachO(name string) (*executable, error) {
	file, err := macho.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if file.Symtab == nil {
		return nil, errors.New("Mach-O symbol table is missing")
	}
	for _, load := range file.Loads {
		raw := load.Raw()
		if len(raw) >= 4 && file.ByteOrder.Uint32(raw[:4]) == loadCmdDyldChainedFixups {
			return nil, errors.New("Mach-O chained fixups are unsupported; release builds must disable them")
		}
	}

	result := &executable{byteOrder: file.ByteOrder, symbols: make(map[string]uint64)}
	for _, symbol := range file.Symtab.Syms {
		if err := result.addSymbol(symbol.Name, symbol.Value); err != nil {
			return nil, err
		}
	}
	for _, source := range file.Sections {
		if source.Offset == 0 || source.Size == 0 {
			continue
		}
		data, err := source.Data()
		if err != nil {
			return nil, fmt.Errorf("read Mach-O section %s: %w", source.Name, err)
		}
		result.sections = append(result.sections, section{address: source.Addr, data: data})
	}
	return result, nil
}

func openPE(name string) (*executable, error) {
	file, err := pe.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	header, ok := file.OptionalHeader.(*pe.OptionalHeader64)
	if !ok {
		return nil, errors.New("PE executable is not 64-bit")
	}

	result := &executable{byteOrder: binary.LittleEndian, symbols: make(map[string]uint64)}
	for _, symbol := range file.Symbols {
		if symbol.SectionNumber <= 0 || int(symbol.SectionNumber) > len(file.Sections) {
			continue
		}
		source := file.Sections[symbol.SectionNumber-1]
		address := header.ImageBase + uint64(source.VirtualAddress) + uint64(symbol.Value)
		if err := result.addSymbol(symbol.Name, address); err != nil {
			return nil, err
		}
	}
	for _, source := range file.Sections {
		data, err := source.Data()
		if err != nil {
			return nil, fmt.Errorf("read PE section %s: %w", source.Name, err)
		}
		result.sections = append(result.sections, section{
			address: header.ImageBase + uint64(source.VirtualAddress),
			data:    data,
		})
	}
	return result, nil
}

func (file *executable) addSymbol(name string, address uint64) error {
	name = strings.TrimPrefix(name, "_")
	if !isReleaseSymbol(name) {
		return nil
	}
	if _, exists := file.symbols[name]; exists {
		return fmt.Errorf("duplicate symbol %s", name)
	}
	file.symbols[name] = address
	return nil
}

func isReleaseSymbol(name string) bool {
	switch name {
	case "main.version", "main.version.str",
		"main.releaseLinkerSetting", "main.releaseLinkerSetting.str":
		return true
	default:
		return false
	}
}

func (file *executable) goString(name string) (string, error) {
	headerAddress, ok := file.symbols[name]
	if !ok {
		return "", fmt.Errorf("missing symbol %s", name)
	}
	dataAddress, ok := file.symbols[name+".str"]
	if !ok {
		return "", fmt.Errorf("missing symbol %s.str", name)
	}
	header, err := file.bytesAt(headerAddress, 16)
	if err != nil {
		return "", fmt.Errorf("read string header: %w", err)
	}
	pointer := file.byteOrder.Uint64(header[:8])
	length := file.byteOrder.Uint64(header[8:])
	if pointer != dataAddress {
		return "", fmt.Errorf("string data pointer %#x does not match symbol %#x", pointer, dataAddress)
	}
	if length > maxReleaseStringLength {
		return "", fmt.Errorf("string length %d exceeds limit", length)
	}
	value, err := file.bytesAt(pointer, length)
	if err != nil {
		return "", fmt.Errorf("read string data: %w", err)
	}
	if !utf8.Valid(value) {
		return "", errors.New("string data is not UTF-8")
	}
	return string(value), nil
}

func (file *executable) bytesAt(address, size uint64) ([]byte, error) {
	if address+size < address {
		return nil, errors.New("address range overflows")
	}
	for _, candidate := range file.sections {
		if address < candidate.address {
			continue
		}
		offset := address - candidate.address
		if offset <= uint64(len(candidate.data)) && size <= uint64(len(candidate.data))-offset {
			return candidate.data[offset : offset+size], nil
		}
	}
	return nil, fmt.Errorf("address range %#x..%#x is not file-backed", address, address+size)
}
