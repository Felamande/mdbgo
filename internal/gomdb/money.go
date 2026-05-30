package gomdb

const (
	maxMoneyPrecision   = 20
	maxNumericPrecision = 40
)

// MoneyToString converts an 8-byte Currency field to a string.
// Currency is stored as an 8-byte signed integer scaled by 10^4.
// Negative values use two's complement.
func MoneyToString(buf []byte, offset int) string {
	const numBytes, scale = 8, 4

	bytes := make([]byte, numBytes)
	copy(bytes, buf[offset:offset+numBytes])

	neg := false
	// Check for negative (high bit of last byte set)
	if bytes[numBytes-1]&0x80 != 0 {
		neg = true
		// Two's complement
		for i := 0; i < numBytes; i++ {
			bytes[i] = ^bytes[i]
		}
		for i := 0; i < numBytes; i++ {
			bytes[i]++
			if bytes[i] != 0 {
				break
			}
		}
	}

	multiplier := make([]byte, maxMoneyPrecision)
	multiplier[0] = 1
	product := make([]byte, maxMoneyPrecision)

	for i := 0; i < numBytes; i++ {
		multiplyByte(product, int(bytes[i]), multiplier)
		// multiplier = multiplier * 256
		temp := make([]byte, maxMoneyPrecision)
		copy(temp, multiplier)
		for j := range multiplier {
			multiplier[j] = 0
		}
		multiplyByte(multiplier, 256, temp)
	}

	return arrayToString(product, scale, neg)
}

// NumericToString converts a 16-byte Numeric field to a string.
// The first byte contains a sign flag (0x80 = negative).
// The remaining 16 bytes are stored in a specific byte order.
func NumericToString(buf []byte, start int, scale, prec int) string {
	const numBytes = 16

	neg := false
	if buf[start]&0x80 != 0 {
		neg = true
	}

	bytes := make([]byte, numBytes)
	// Byte order: [12-4*(i/4)+i%4] for i=0..15
	for i := 0; i < numBytes; i++ {
		bytes[i] = buf[start+1+12-4*(i/4)+i%4]
	}

	multiplier := make([]byte, maxNumericPrecision)
	multiplier[0] = 1
	product := make([]byte, maxNumericPrecision)

	for i := 0; i < numBytes; i++ {
		multiplyByte(product, int(bytes[i]), multiplier)
		// multiplier = multiplier * 256
		temp := make([]byte, maxNumericPrecision)
		copy(temp, multiplier)
		for j := range multiplier {
			multiplier[j] = 0
		}
		multiplyByte(multiplier, 256, temp)
	}

	return arrayToString(product, prec, neg)
}

// multiplyByte multiplies a multi-digit product by a single number.
// num is broken into ones, tens, hundreds digits.
func multiplyByte(product []byte, num int, multiplier []byte) {
	number := [3]byte{byte(num % 10), byte((num / 10) % 10), byte((num / 100) % 10)}
	for i := 0; i < len(multiplier); i++ {
		if multiplier[i] == 0 {
			continue
		}
		for j := 0; j < 3 && i+j < len(product); j++ {
			if number[j] == 0 {
				continue
			}
			product[i+j] += multiplier[i] * number[j]
		}
		doCarry(product)
	}
}

// doCarry propagates carries through the product array.
func doCarry(product []byte) {
	for j := 0; j < len(product)-1; j++ {
		if product[j] > 9 {
			product[j+1] += product[j] / 10
			product[j] %= 10
		}
	}
	// Last digit overflow is discarded
	if product[len(product)-1] > 9 {
		product[len(product)-1] %= 10
	}
}

// arrayToString converts a BCD-like digit array to a decimal string.
func arrayToString(array []byte, scale int, neg bool) string {
	// Find the top non-zero digit (above the scale position)
	top := len(array)
	for top > 0 && top-1 > scale && array[top-1] == 0 {
		top--
	}

	// Calculate buffer size: sign + digits + decimal point + null
	buf := make([]byte, 0, len(array)+3)

	if neg {
		buf = append(buf, '-')
	}

	if top == 0 {
		buf = append(buf, '0')
	} else {
		for i := top; i > 0; i-- {
			if i == scale {
				buf = append(buf, '.')
			}
			buf = append(buf, array[i-1]+'0')
		}
	}

	return string(buf)
}
