package bluray

// Reed-Solomon over GF(256), polynomial 0x11d, roots alpha^0 onward
// (shortened systematic codes used by BD address and LDC/BIS fields).

var (
	rsExponents   = buildRsExponents()
	rsLogarithms  = buildRsLogarithms()
	rsGenerator4  = buildRsGenerator(4)
	rsGenerator32 = buildRsGenerator(32)
)

// RsComputeParity writes the 4 address-field or 32 LDC/BIS parity bytes in
// on-disc order.
func RsComputeParity(information, parity []byte) error {
	if len(parity) != 4 && len(parity) != 32 {
		return argErr("BD address and LDC/BIS codes use 4 or 32 parity bytes.", "parity")
	}
	if len(information) == 0 || len(information)+len(parity) > 255 {
		return argErr("The shortened code word must contain 1..255 symbols.", "information")
	}
	generator := rsGenerator4
	if len(parity) == 32 {
		generator = rsGenerator32
	}
	var work [255]byte
	copy(work[:], information)
	for i := 0; i < len(information); i++ {
		leading := work[i]
		if leading == 0 {
			continue
		}
		for coefficient := 1; coefficient < len(generator); coefficient++ {
			work[i+coefficient] ^= rsMultiply(leading, generator[coefficient])
		}
	}
	copy(parity, work[len(information):len(information)+len(parity)])
	return nil
}

func rsMultiply(left, right byte) byte {
	if left == 0 || right == 0 {
		return 0
	}
	return rsExponents[int(rsLogarithms[left])+int(rsLogarithms[right])]
}

func buildRsGenerator(degree int) []byte {
	result := []byte{1}
	for root := 0; root < degree; root++ {
		next := make([]byte, len(result)+1)
		for i := 0; i < len(result); i++ {
			next[i] ^= result[i]
			next[i+1] ^= rsMultiply(result[i], rsExponents[root])
		}
		result = next
	}
	return result
}

func buildRsExponents() [510]byte {
	var result [510]byte
	value := 1
	for i := 0; i < 255; i++ {
		result[i] = byte(value)
		value <<= 1
		if value&0x100 != 0 {
			value ^= 0x11d
		}
	}
	for i := 255; i < len(result); i++ {
		result[i] = result[i-255]
	}
	return result
}

func buildRsLogarithms() [256]byte {
	var result [256]byte
	for i := 0; i < 255; i++ {
		result[rsExponents[i]] = byte(i)
	}
	return result
}
