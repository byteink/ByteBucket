package util

import (
	"crypto/rand"
	"math/big"
)

const (
	AccessKeyCharset       = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	SecretAccessKeyCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789/+"
)

// GenerateRandomString returns a random string of length n from the given charset.
func GenerateRandomString(n int, charset string) string {
	result := make([]byte, n)
	charsetLen := big.NewInt(int64(len(charset)))
	for i := 0; i < n; i++ {
		idx, err := rand.Int(rand.Reader, charsetLen)
		if err != nil {
			panic(err) // This should never happen.
		}
		result[i] = charset[idx.Int64()]
	}
	return string(result)
}
