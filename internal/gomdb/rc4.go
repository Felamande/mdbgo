package gomdb

// rc4Key holds the state for an RC4 cipher.
type rc4Key struct {
	state [256]byte
	x, y  byte
}

// rc4SetKey initializes the RC4 key state from the provided key data.
func rc4SetKey(key []byte) *rc4Key {
	k := &rc4Key{}
	for i := 0; i < 256; i++ {
		k.state[i] = byte(i)
	}
	var index1, index2 byte
	for counter := 0; counter < 256; counter++ {
		index2 = key[index1] + k.state[counter] + index2
		k.state[counter], k.state[index2] = k.state[index2], k.state[counter]
		index1 = (index1 + 1) % byte(len(key))
	}
	return k
}

// rc4Crypt performs RC4 encryption/decryption in-place on buf.
func (k *rc4Key) rc4Crypt(buf []byte) {
	x, y := int(k.x), int(k.y)
	for counter := 0; counter < len(buf); counter++ {
		x = (x + 1) % 256
		y = (int(k.state[x]) + y) % 256
		k.state[x], k.state[y] = k.state[y], k.state[x]
		xorIdx := (int(k.state[x]) + int(k.state[y])) % 256
		buf[counter] ^= k.state[xorIdx]
	}
	k.x, k.y = byte(x), byte(y)
}

// rc4Decrypt is a convenience function that decrypts a buffer using RC4 with the given key.
// It performs the decryption in-place.
func rc4Decrypt(key, buf []byte) {
	k := rc4SetKey(key)
	k.rc4Crypt(buf)
}
