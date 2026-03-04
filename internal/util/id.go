// Package util provides shared utilities.
package util

import (
	"crypto/rand"
	"fmt"
)

// NewID generates a random ID with the given prefix (e.g., "task_", "run_").
func NewID(prefix string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand.Read failed: %v", err))
	}
	return prefix + fmt.Sprintf("%x", b)
}
