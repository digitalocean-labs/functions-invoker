package units

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ByteSize is an IEC representation of data in bytes.
type ByteSize uint64

const (
	Byte     ByteSize = 1
	Kilobyte ByteSize = 1024
	Megabyte ByteSize = 1024 * 1024
	Gigabyte ByteSize = 1024 * 1024 * 1024
)

// ParseByteSize parses a string into a ByteSize.
func ParseByteSize(s string) (ByteSize, error) {
	i := strings.IndexFunc(s, unicode.IsLetter)
	if i == -1 {
		return 0, errors.New("failed to find unit")
	}

	bytesString, unit := strings.TrimSpace(s[:i]), strings.TrimSpace(s[i:])
	bytes, err := strconv.ParseUint(bytesString, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("failed to parse number: %w", err)
	}

	switch strings.ToUpper(unit) {
	case "G", "GB", "GIB":
		return ByteSize(bytes) * Gigabyte, nil
	case "M", "MB", "MIB":
		return ByteSize(bytes) * Megabyte, nil
	case "K", "KB", "KIB":
		return ByteSize(bytes) * Kilobyte, nil
	case "B":
		return ByteSize(bytes) * Byte, nil
	default:
		return 0, fmt.Errorf("invalid unit %q", unit)
	}
}

// String turns the given ByteSize into a human readable form. It uses the largest unit that
// doesn't require a float representation.
func (bs ByteSize) String() string {
	if bs%Gigabyte == 0 {
		return fmt.Sprintf("%d GB", bs/Gigabyte)
	}
	if bs%Megabyte == 0 {
		return fmt.Sprintf("%d MB", bs/Megabyte)
	}
	if bs%Kilobyte == 0 {
		return fmt.Sprintf("%d KB", bs/Kilobyte)
	}

	return fmt.Sprintf("%d B", bs)
}

// Decode implements `envconfig.Decoder`.
func (bs *ByteSize) Decode(s string) error {
	parsed, err := ParseByteSize(s)
	if err != nil {
		return err
	}
	*bs = parsed
	return nil
}

// UnmarshallJSON implements the Golang stdlib unmarshaller.
func (bs *ByteSize) UnmarshalJSON(data []byte) error {
	// We expect data to be a string. Otherwise we fail early.
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return fmt.Errorf("failed to parse data into a string: %w", err)
	}

	parsed, err := ParseByteSize(str)
	if err != nil {
		return err
	}
	*bs = parsed
	return nil
}

// GigabyteSeconds computes gigabytes per second (duration x function memory reserved).
func GigabyteSeconds(memory ByteSize, duration time.Duration) float64 {
	//
	// the formula is:
	//
	//     megabytes       quanta * 100 ms
	//   ------------- *  -----------------
	//     1024 mg/gb         1000 ms/s
	//
	// which we simplify to:
	//
	//     megabytes * quanta              100 ms
	//   ---------------------- * ----------------------------
	//              1              1024 * 1000 (mg ms / gb s)
	//
	// reducing the second term to 1 / 1024 * 10 = 1 / 10240
	//
	quanta := math.Ceil(float64(duration) / float64(time.Millisecond) / float64(100)) // 100ms quantum
	return float64(memory/Megabyte) * quanta / float64(10240)
}
