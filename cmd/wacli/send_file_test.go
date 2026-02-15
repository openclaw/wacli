package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestGenerateWaveform_EmptyData(t *testing.T) {
	wf := generateWaveform(nil)
	if len(wf) != 64 {
		t.Fatalf("expected 64 samples, got %d", len(wf))
	}
	for i, v := range wf {
		if v != 0 {
			t.Fatalf("expected 0 at index %d, got %d", i, v)
		}
	}
}

func TestGenerateWaveform_Length(t *testing.T) {
	// Create fake audio data (16-bit LE samples)
	data := make([]byte, 64*100*2) // 100 samples per chunk, 64 chunks
	for i := 0; i < len(data); i += 2 {
		binary.LittleEndian.PutUint16(data[i:i+2], 1000)
	}
	wf := generateWaveform(data)
	if len(wf) != 64 {
		t.Fatalf("expected 64 samples, got %d", len(wf))
	}
}

func TestGenerateWaveform_ValuesInRange(t *testing.T) {
	// Create data with varying amplitude
	var buf bytes.Buffer
	for i := 0; i < 64*200; i++ {
		sample := int16(i % 32768)
		_ = binary.Write(&buf, binary.LittleEndian, sample)
	}
	wf := generateWaveform(buf.Bytes())
	if len(wf) != 64 {
		t.Fatalf("expected 64 samples, got %d", len(wf))
	}
	for i, v := range wf {
		if v > 100 {
			t.Fatalf("sample %d out of range: %d", i, v)
		}
	}
}

func TestGenerateWaveform_NotAllZero(t *testing.T) {
	// Non-silent audio should produce non-zero waveform
	var buf bytes.Buffer
	for i := 0; i < 64*200; i++ {
		sample := int16((i * 137) % 20000) // varying amplitude
		_ = binary.Write(&buf, binary.LittleEndian, sample)
	}
	wf := generateWaveform(buf.Bytes())
	hasNonZero := false
	for _, v := range wf {
		if v > 0 {
			hasNonZero = true
			break
		}
	}
	if !hasNonZero {
		t.Fatal("expected non-zero waveform for non-silent audio")
	}
}

func TestGenerateWaveform_MaxIs100(t *testing.T) {
	// Uniform loud signal should have all samples at 100
	data := make([]byte, 64*100*2)
	for i := 0; i < len(data); i += 2 {
		binary.LittleEndian.PutUint16(data[i:i+2], uint16(10000))
	}
	wf := generateWaveform(data)
	for i, v := range wf {
		if v != 100 {
			t.Fatalf("expected 100 at index %d, got %d (uniform signal)", i, v)
		}
	}
}

func TestGenerateWaveform_SmallData(t *testing.T) {
	// Very small data (less than 128 bytes = 64*2)
	data := []byte{0x00, 0x10, 0xFF, 0x7F}
	wf := generateWaveform(data)
	if len(wf) != 64 {
		t.Fatalf("expected 64 samples, got %d", len(wf))
	}
}
