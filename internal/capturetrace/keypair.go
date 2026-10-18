package capturetrace

import (
	"crypto/aes"
	"crypto/cipher"
)

type keyPair struct {
	aead cipher.AEAD
	iv   [12]byte
}

// makePair uses the vendor's fixed IV contract. This code only decrypts; it
// neither generates material nor reuses a key/IV to send new ciphertext.
func makePair(key, iv []byte) (*keyPair, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &keyPair{aead, [12]byte(iv)}, nil
}
