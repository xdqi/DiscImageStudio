package dvd

import (
	"crypto/rand"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// dotnetRandom ports System.Random's legacy seeded subtractive algorithm
// (the constructor with an explicit seed never uses the xoshiro family).
type dotnetRandom struct {
	seedArray [56]int32
	inext     int32
	inextp    int32
}

func newDotnetRandom(seed int32) *dotnetRandom {
	r := &dotnetRandom{}
	var subtraction int64
	if seed == math.MinInt32 {
		subtraction = math.MaxInt32
	} else if seed < 0 {
		subtraction = -int64(seed)
	} else {
		subtraction = int64(seed)
	}
	mj := int32(161803398 - subtraction)
	r.seedArray[55] = mj
	mk := int32(1)
	for i := int32(1); i < 55; i++ {
		ii := (21 * i) % 55
		r.seedArray[ii] = mk
		mk = mj - mk
		if mk < 0 {
			mk += math.MaxInt32
		}
		mj = r.seedArray[ii]
	}
	for k := 1; k < 5; k++ {
		for i := int32(1); i < 56; i++ {
			r.seedArray[i] -= r.seedArray[1+(i+30)%55]
			if r.seedArray[i] < 0 {
				r.seedArray[i] += math.MaxInt32
			}
		}
	}
	r.inext = 0
	r.inextp = 21
	return r
}

func (r *dotnetRandom) internalSample() int32 {
	locINext := r.inext + 1
	if locINext >= 56 {
		locINext = 1
	}
	locINextp := r.inextp + 1
	if locINextp >= 56 {
		locINextp = 1
	}
	retVal := r.seedArray[locINext] - r.seedArray[locINextp]
	if retVal == math.MaxInt32 {
		retVal--
	}
	if retVal < 0 {
		retVal += math.MaxInt32
	}
	r.seedArray[locINext] = retVal
	r.inext = locINext
	r.inextp = locINextp
	return retVal
}

func (r *dotnetRandom) sample() float64 {
	return float64(r.internalSample()) * (1.0 / 2147483647.0)
}

func (r *dotnetRandom) next() int32 { return r.internalSample() }

func (r *dotnetRandom) nextMax(maxValue int32) int32 {
	return int32(r.sample() * float64(maxValue))
}

func (r *dotnetRandom) nextRange(minValue, maxValue int32) int32 {
	span := int64(maxValue) - int64(minValue)
	return int32(int64(r.sample()*float64(span)) + int64(minValue))
}

func (r *dotnetRandom) nextBytes(buffer []byte) {
	for i := range buffer {
		buffer[i] = byte(r.internalSample() % 256)
	}
}

func (r *dotnetRandom) nextDouble() float64 { return r.sample() }

// formatHexUpper ports the C# "X" format with an optional minimum width.
func formatHexUpper(value uint64, minDigits int) string {
	s := strconv.FormatUint(value, 16)
	s = strings.ToUpper(s)
	if len(s) < minDigits {
		s = strings.Repeat("0", minDigits-len(s)) + s
	}
	return s
}

// formatFixed ports the C# "F<n>" format: fixed-point with round-half-even.
func formatFixed(value float64, decimals int) string {
	return strconv.FormatFloat(value, 'f', decimals, 64)
}

// formatPercentP0 ports the C# "P0" format used by the solver diagnostics.
func formatPercentP0(value float64) string {
	return formatFixed(value*100, 0) + "%"
}

// formatDoubleG ports the shortest round-trippable "G" formatting
// System.Text.Json applies to double values.
func formatDoubleG(value float64) string {
	if value == 0 {
		if math.Signbit(value) {
			return "-0"
		}
		return "0"
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		// System.Text.Json throws on non-finite values; unreachable in practice.
		return "0"
	}
	encoded := strconv.FormatFloat(value, 'e', -1, 64)
	// encoded looks like "-d.dddde±XX" or "de±XX".
	negative := strings.HasPrefix(encoded, "-")
	body := strings.TrimPrefix(encoded, "-")
	eIndex := strings.IndexByte(body, 'e')
	mantissa := body[:eIndex]
	exponent, _ := strconv.Atoi(body[eIndex+1:])
	digits := strings.Replace(mantissa, ".", "", 1)
	// .NET picks fixed notation when -4 <= exponent < 15, scientific otherwise.
	if exponent >= 15 || exponent < -4 {
		var b strings.Builder
		if negative {
			b.WriteByte('-')
		}
		b.WriteByte(digits[0])
		if len(digits) > 1 {
			b.WriteByte('.')
			b.WriteString(digits[1:])
		}
		b.WriteByte('E')
		if exponent < 0 {
			b.WriteByte('-')
		} else {
			b.WriteByte('+')
		}
		absExp := exponent
		if absExp < 0 {
			absExp = -absExp
		}
		expText := strconv.Itoa(absExp)
		if len(expText) < 2 {
			expText = "0" + expText
		}
		b.WriteString(expText)
		return b.String()
	}
	var b strings.Builder
	if negative {
		b.WriteByte('-')
	}
	decimalPoint := exponent + 1
	if decimalPoint <= 0 {
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", -decimalPoint))
		b.WriteString(digits)
	} else if decimalPoint >= len(digits) {
		b.WriteString(digits)
		b.WriteString(strings.Repeat("0", decimalPoint-len(digits)))
	} else {
		b.WriteString(digits[:decimalPoint])
		b.WriteByte('.')
		b.WriteString(digits[decimalPoint:])
	}
	return b.String()
}

// newGuidN returns a 32-digit lowercase hex identifier like Guid.NewGuid():N.
func newGuidN() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", b[:])
}
