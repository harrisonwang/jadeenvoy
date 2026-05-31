// Package vault 是凭据存储 + MITM 注入。V1 仅 static_bearer（ADR-0015）。
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
)

// cipherBox 用 AES-256-GCM 加解密凭据明文。
//
// key 由 PLATFORM_ROOT_SECRET 派生（sha256 → 32 字节），cipher_label 作为
// GCM 的 additionalData（AAD），把密文绑定到用途上下文（例 "vault.credential.token"），
// 防止密文被挪用到别的字段。
type cipherBox struct {
	gcm cipher.AEAD
}

func newCipherBox(rootSecret string) (*cipherBox, error) {
	key := sha256.Sum256([]byte(rootSecret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	return &cipherBox{gcm: gcm}, nil
}

// Encrypt 用 label 作为 AAD 加密 plaintext，返回 (cipher, nonce)。
func (c *cipherBox) Encrypt(plaintext []byte, label string) (cipherText, nonce []byte, err error) {
	nonce = make([]byte, c.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("nonce: %w", err)
	}
	cipherText = c.gcm.Seal(nil, nonce, plaintext, []byte(label))
	return cipherText, nonce, nil
}

// Decrypt 用 label 作为 AAD 解密。label / cipher / nonce 任一不匹配都会失败。
func (c *cipherBox) Decrypt(cipherText, nonce []byte, label string) ([]byte, error) {
	plaintext, err := c.gcm.Open(nil, nonce, cipherText, []byte(label))
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}
	return plaintext, nil
}
