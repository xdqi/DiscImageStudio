package dvd

import (
	"math"
	"strconv"
	"strings"
	"unicode"
)

// cliArguments ports CliArguments: --key value or --key=value options with
// case-insensitive keys and duplicate rejection. Keys passed in
// repeatableKeys may be given multiple times; retrieve all values with
// GetAll (Get returns the first).
type cliArguments struct {
	values map[string]string
	multi  map[string][]string
}

func parseCliArguments(arguments []string, repeatableKeys ...string) (*cliArguments, error) {
	repeatable := make(map[string]bool, len(repeatableKeys))
	for _, key := range repeatableKeys {
		repeatable[strings.ToLower(key)] = true
	}
	values := make(map[string]string)
	multi := make(map[string][]string)
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if !strings.HasPrefix(argument, "--") {
			return nil, newArgumentError("Unexpected argument '" + argument + "'. Options must start with --.")
		}
		var key, value string
		if equals := strings.IndexByte(argument, '='); equals >= 0 {
			key = argument[2:equals]
			value = argument[equals+1:]
		} else {
			key = argument[2:]
			index++
			if index >= len(arguments) {
				return nil, newArgumentError("Missing value for --" + key + ".")
			}
			value = arguments[index]
		}
		lower := strings.ToLower(key) // OrdinalIgnoreCase
		if repeatable[lower] {
			if _, exists := values[lower]; !exists {
				values[lower] = value
			}
			multi[lower] = append(multi[lower], value)
			continue
		}
		if _, exists := values[lower]; exists {
			return nil, newArgumentError("Option --" + key + " was specified more than once.")
		}
		values[lower] = value
	}
	return &cliArguments{values: values, multi: multi}, nil
}

func (a *cliArguments) Require(key string) (string, error) {
	if value, ok := a.values[strings.ToLower(key)]; ok {
		return value, nil
	}
	return "", newArgumentError("Missing required option --" + key + ".")
}

// RequireValue panics are avoided by callers that check Has first; this is the
// direct Require equivalent returning an error.
func (a *cliArguments) Get(key, fallback string) string {
	if value, ok := a.values[strings.ToLower(key)]; ok {
		return value
	}
	return fallback
}

func (a *cliArguments) Has(key string) bool {
	_, ok := a.values[strings.ToLower(key)]
	return ok
}

// GetAll returns every value given for a repeatable option, in order.
func (a *cliArguments) GetAll(key string) []string {
	return a.multi[strings.ToLower(key)]
}

func (a *cliArguments) GetInt(key string, fallback int) (int, error) {
	value, ok := a.values[strings.ToLower(key)]
	if !ok {
		return fallback, nil
	}
	return parseInt32(value)
}

func (a *cliArguments) GetUInt(key string, fallback uint32) (uint32, error) {
	value, ok := a.values[strings.ToLower(key)]
	if !ok {
		return fallback, nil
	}
	return parseUInt32(value)
}

func (a *cliArguments) GetDouble(key string, fallback float64) (float64, error) {
	value, ok := a.values[strings.ToLower(key)]
	if !ok {
		return fallback, nil
	}
	return parseDouble(value)
}

func (a *cliArguments) GetBoolean(key string, fallback bool) (bool, error) {
	value, ok := a.values[strings.ToLower(key)]
	if !ok {
		return fallback, nil
	}
	return parseBool(value)
}

// dotnetTrimSpace trims what NumberStyles whitespace allowances accept.
func dotnetTrimSpace(s string) string {
	return strings.TrimFunc(s, unicode.IsSpace)
}

// parseInt32 mirrors int.Parse(value, NumberStyles.Integer, InvariantCulture).
func parseInt32(value string) (int, error) {
	trimmed := dotnetTrimSpace(value)
	digits := trimmed
	if strings.HasPrefix(digits, "+") {
		digits = digits[1:]
	} else if strings.HasPrefix(digits, "-") {
		digits = digits[1:]
	}
	if digits == "" {
		return 0, &formatError{"The input string '" + value + "' was not in a correct format."}
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, &formatError{"The input string '" + value + "' was not in a correct format."}
		}
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, &formatError{"The input string '" + value + "' was not in a correct format."}
	}
	if parsed > 2147483647 || parsed < -2147483648 {
		return 0, newOverflowError("Value was either too large or too small for an Int32.")
	}
	return int(parsed), nil
}

// parseUInt32 mirrors uint.Parse with the 0x-prefixed hex fast path.
func parseUInt32(value string) (uint32, error) {
	if len(value) >= 2 && value[0] == '0' && (value[1] == 'x' || value[1] == 'X') {
		hex := value[2:]
		trimmed := dotnetTrimSpace(hex)
		if trimmed == "" {
			return 0, &formatError{"The input string '" + hex + "' was not in a correct format."}
		}
		for i := 0; i < len(trimmed); i++ {
			c := trimmed[i]
			if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
				return 0, &formatError{"The input string '" + hex + "' was not in a correct format."}
			}
		}
		parsed, err := strconv.ParseUint(trimmed, 16, 64)
		if err != nil {
			return 0, &formatError{"The input string '" + hex + "' was not in a correct format."}
		}
		if parsed > 0xFFFFFFFF {
			return 0, newOverflowError("Value was either too large or too small for a UInt32.")
		}
		return uint32(parsed), nil
	}
	trimmed := dotnetTrimSpace(value)
	digits := trimmed
	if strings.HasPrefix(digits, "+") {
		digits = digits[1:]
	}
	if digits == "" {
		return 0, &formatError{"The input string '" + value + "' was not in a correct format."}
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, &formatError{"The input string '" + value + "' was not in a correct format."}
		}
	}
	parsed, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil {
		// A negative sign parses but is rejected here; treat as overflow like .NET.
		if strings.HasPrefix(trimmed, "-") {
			return 0, newOverflowError("Value was either too large or too small for a UInt32.")
		}
		return 0, &formatError{"The input string '" + value + "' was not in a correct format."}
	}
	if parsed > 0xFFFFFFFF {
		return 0, newOverflowError("Value was either too large or too small for a UInt32.")
	}
	return uint32(parsed), nil
}

// parseDouble mirrors double.Parse(value, NumberStyles.Float, InvariantCulture).
func parseDouble(value string) (float64, error) {
	trimmed := dotnetTrimSpace(value)
	if trimmed == "" {
		return 0, &formatError{"The input string '" + value + "' was not in a correct format."}
	}
	switch trimmed {
	case "NaN":
		return nanValue(), nil
	case "Infinity", "+Infinity":
		return infValue(1), nil
	case "-Infinity":
		return infValue(-1), nil
	}
	// Validate the NumberStyles.Float grammar: [sign] digits[.digits][e[sign]digits]
	if !isValidFloatGrammar(trimmed) {
		return 0, &formatError{"The input string '" + value + "' was not in a correct format."}
	}
	parsed, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		if numErr, ok := err.(*strconv.NumError); ok && numErr.Err == strconv.ErrRange {
			if strings.HasPrefix(trimmed, "-") {
				return infValue(-1), nil // .NET Core 3+ returns -Infinity on overflow.
			}
			return infValue(1), nil
		}
		return 0, &formatError{"The input string '" + value + "' was not in a correct format."}
	}
	return parsed, nil
}

func isValidFloatGrammar(s string) bool {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	digitsBefore := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
		digitsBefore++
	}
	digitsAfter := 0
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			digitsAfter++
		}
	}
	if digitsBefore+digitsAfter == 0 {
		return false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		expDigits := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			expDigits++
		}
		if expDigits == 0 {
			return false
		}
	}
	return i == len(s)
}

// parseBool mirrors bool.Parse (whitespace-trimmed, case-insensitive).
func parseBool(value string) (bool, error) {
	trimmed := dotnetTrimSpace(value)
	if strings.EqualFold(trimmed, "true") {
		return true, nil
	}
	if strings.EqualFold(trimmed, "false") {
		return false, nil
	}
	return false, &formatError{"String '" + value + "' was not recognized as a valid Boolean."}
}

// hexDumpUpper mirrors Convert.ToHexString (uppercase, no separators).
func hexDumpUpper(data []byte) string {
	const digits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(data) * 2)
	for _, v := range data {
		b.WriteByte(digits[v>>4])
		b.WriteByte(digits[v&0xF])
	}
	return b.String()
}

func nanValue() float64 { return math.NaN() }
func infValue(sign int) float64 {
	return math.Inf(sign)
}
