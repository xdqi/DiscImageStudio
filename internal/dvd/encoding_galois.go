package dvd

// galoisField256 ports GaloisField256 (GF(2^8) with polynomial 0x11D).
type galoisField256Tables struct {
	exp [512]byte
	log [256]byte
}

var galoisField256 = buildGaloisField256()

func buildGaloisField256() *galoisField256Tables {
	t := &galoisField256Tables{}
	value := 1
	for exponent := 0; exponent < 255; exponent++ {
		t.exp[exponent] = byte(value)
		t.log[value] = byte(exponent)
		value <<= 1
		if value&0x100 != 0 {
			value ^= 0x11D
		}
	}
	for exponent := 255; exponent < len(t.exp); exponent++ {
		t.exp[exponent] = t.exp[exponent-255]
	}
	return t
}

func (t *galoisField256Tables) multiply(left, right byte) byte {
	if left == 0 || right == 0 {
		return 0
	}
	return t.exp[int(t.log[left])+int(t.log[right])]
}

func (t *galoisField256Tables) alphaPower(exponent int) byte {
	return t.exp[((exponent%255)+255)%255]
}

// reedSolomon ports ReedSolomon.
type reedSolomonTables struct {
	piGenerator  []byte
	poGenerator  []byte
	iedGenerator []byte
}

var reedSolomon = &reedSolomonTables{
	piGenerator:  buildRsGenerator(10),
	poGenerator:  buildRsGenerator(16),
	iedGenerator: buildRsGenerator(2),
}

// The append wrappers never fail: callers pass parity slices whose length
// matches the fixed generators, exactly like the C# void methods.
func rsAppendPi(data, parity []byte)  { _ = rsEncode(data, parity, reedSolomon.piGenerator) }
func rsAppendPo(data, parity []byte)  { _ = rsEncode(data, parity, reedSolomon.poGenerator) }
func rsAppendIed(data, parity []byte) { _ = rsEncode(data, parity, reedSolomon.iedGenerator) }

func rsHasZeroRemainder(codeword []byte, parityBytes int) bool {
	remainder := make([]byte, parityBytes)
	var generator []byte
	switch parityBytes {
	case 2:
		generator = reedSolomon.iedGenerator
	case 10:
		generator = reedSolomon.piGenerator
	case 16:
		generator = reedSolomon.poGenerator
	default:
		generator = buildRsGenerator(parityBytes)
	}
	rsEncode(codeword, remainder, generator)
	for _, b := range remainder {
		if b != 0 {
			return false
		}
	}
	return true
}

func rsEncode(data []byte, parity []byte, generator []byte) error {
	if len(parity) != len(generator)-1 {
		return newArgumentErrorParam("Parity and generator sizes do not match.", "parity")
	}
	for i := range parity {
		parity[i] = 0
	}
	for _, value := range data {
		feedback := value ^ parity[0]
		for index := 0; index < len(parity)-1; index++ {
			parity[index] = parity[index+1] ^ galoisField256.multiply(feedback, generator[index+1])
		}
		parity[len(parity)-1] = galoisField256.multiply(feedback, generator[len(generator)-1])
	}
	return nil
}

func buildRsGenerator(parityBytes int) []byte {
	polynomial := []byte{1}
	for root := 0; root < parityBytes; root++ {
		next := make([]byte, len(polynomial)+1)
		alpha := galoisField256.alphaPower(root)
		for index := 0; index < len(polynomial); index++ {
			next[index] ^= polynomial[index]
			next[index+1] ^= galoisField256.multiply(polynomial[index], alpha)
		}
		polynomial = next
	}
	return polynomial
}
