package main

import (
	"encoding/base64"
	"fmt"
)

// decodeB64 decodes a base64 string (standard encoding) into raw bytes.
func decodeB64(s string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("empty base64 payload")
	}
	return base64.StdEncoding.DecodeString(s)
}
