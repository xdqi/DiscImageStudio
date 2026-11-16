package bluray

import (
	"fmt"
	"math"
	"strings"
)

// The helpers below reproduce the text of the corresponding .NET exception
// messages, including the " (Parameter 'name')" suffix that ArgumentException
// and ArgumentOutOfRangeException append when a parameter name is supplied.

// argErr ports new ArgumentException(message, paramName).
func argErr(message, paramName string) error {
	return fmt.Errorf("%s (Parameter '%s')", message, paramName)
}

// argOutOfRange ports new ArgumentOutOfRangeException(paramName).
func argOutOfRange(paramName string) error {
	return fmt.Errorf("Specified argument was out of the range of valid values. (Parameter '%s')", paramName)
}

// argOutOfRangeMsg ports new ArgumentOutOfRangeException(paramName, message).
func argOutOfRangeMsg(paramName, message string) error {
	return fmt.Errorf("%s (Parameter '%s')", message, paramName)
}

// argNull ports ArgumentNullException(paramName) / ThrowIfNull.
func argNull(paramName string) error {
	return fmt.Errorf("Value cannot be null. (Parameter '%s')", paramName)
}

// argNullOrWhiteSpace ports ArgumentException.ThrowIfNullOrWhiteSpace for the
// non-null whitespace case.
func argNullOrWhiteSpace(paramName string) error {
	return fmt.Errorf("The value cannot be an empty string or composed entirely of whitespace. (Parameter '%s')", paramName)
}

// argNegativeOrZero ports ArgumentOutOfRangeException.ThrowIfNegativeOrZero.
func argNegativeOrZero(paramName string, value int) error {
	return fmt.Errorf("%s ('%d') must be a non-negative and non-zero value. (Parameter '%s')", paramName, value, paramName)
}

func isNotFinite(v float64) bool { return math.IsNaN(v) || math.IsInf(v, 0) }

func isNullOrWhiteSpace(s string) bool { return strings.TrimSpace(s) == "" }

func equalFold(a, b string) bool { return strings.EqualFold(a, b) }

// clampFloat ports Math.Clamp for float64.
func clampFloat(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func clampInt(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
