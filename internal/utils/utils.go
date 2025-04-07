package utils

import (
	"encoding/json"
)

// PrettifyStruct converts an object to a pretty-printed JSON string
func PrettifyStruct(obj any) string {
	bytes, _ := json.MarshalIndent(obj, "", "  ")
	return string(bytes)
}

// Min returns the minimum of two integers
func Min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
