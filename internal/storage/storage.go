package storage

import (
	"ByteBucket/internal/util"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/boltdb/bolt"
)

var userDB *bolt.DB
var encryptionKey []byte

// Detect if running inside Docker
func isRunningInDocker() bool {
	content, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false // Assume not in Docker if we can't read the file
	}
	return strings.Contains(string(content), "docker") || strings.Contains(string(content), "containerd")
}

// Get the correct storage path based on environment
func getStoragePath(fileName string) string {
	if isRunningInDocker() {
		return "/data/" + fileName
	}
	return "./data/" + fileName
}

// Ensure the data directory exists
func ensureDataDirExists(path string) error {
	dir := path[:strings.LastIndex(path, "/")]
	return os.MkdirAll(dir, 0755)
}

// SetEncryptionKey sets the key used for encryption/decryption.
func SetEncryptionKey(key []byte) {
	encryptionKey = key
}

// InitUserStore initializes BoltDB for user data.
func InitUserStore(fileName string) error {
	dbPath := getStoragePath(fileName)

	// Ensure directory exists
	if err := ensureDataDirExists(dbPath); err != nil {
		return err
	}

	var err error
	userDB, err = bolt.Open(dbPath, 0600, nil)
	if err != nil {
		return err
	}
	return userDB.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte("Users")); err != nil {
			return err
		}
		log.Println("User store initialized at", dbPath)
		return nil
	})
}

// Encrypt encrypts plaintext using AES-GCM and returns a base64-encoded ciphertext.
func Encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", err
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aesGCM.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := aesGCM.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts a base64-encoded ciphertext using AES-GCM.
func Decrypt(cipherText string) (string, error) {
	data, err := base64.StdEncoding.DecodeString(cipherText)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return "", err
	}
	aesGCM, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := aesGCM.NonceSize()
	if len(data) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := aesGCM.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// ACLRule represents an individual policy rule, similar to an IAM statement.
type ACLRule struct {
	Effect  string   `json:"effect"`  // "Allow" or "Deny"
	Buckets []string `json:"buckets"` // e.g. ["*"] or specific bucket names
	Actions []string `json:"actions"` // e.g. ["*"] or specific actions
}

// User represents a user record.
type User struct {
	AccessKeyID     string    `json:"accessKeyID"`
	EncryptedSecret string    `json:"encryptedSecret"`
	ACL             []ACLRule `json:"acl"`
	// CreatedAt is populated when a user is first persisted. Pre-existing
	// records written before this field existed unmarshal to the zero value;
	// handlers render that as unknown.
	CreatedAt time.Time `json:"createdAt,omitempty"`
	// SecretAccessKey is only used temporarily when a user is created.
	SecretAccessKey string `json:"-"`
}

// CreateUser stores a new user in BoltDB.
func CreateUser(user *User) error {
	// Stamp creation time at a single point so every caller (super-user
	// bootstrap, admin API, tests) gets the field without having to remember.
	if user.CreatedAt.IsZero() {
		user.CreatedAt = time.Now().UTC()
	}
	return userDB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("Users"))
		if b.Get([]byte(user.AccessKeyID)) != nil {
			return fmt.Errorf("user already exists")
		}
		data, err := json.Marshal(user)
		if err != nil {
			return err
		}
		return b.Put([]byte(user.AccessKeyID), data)
	})
}

// GetUser retrieves a user by access key.
func GetUser(accessKey string) (*User, error) {
	var user User
	err := userDB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("Users"))
		v := b.Get([]byte(accessKey))
		if v == nil {
			return fmt.Errorf("user not found")
		}
		return json.Unmarshal(v, &user)
	})
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// ListUsers returns all users.
func ListUsers() ([]User, error) {
	var users []User
	err := userDB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("Users"))
		return b.ForEach(func(k, v []byte) error {
			var u User
			if err := json.Unmarshal(v, &u); err != nil {
				return err
			}
			users = append(users, u)
			return nil
		})
	})
	return users, err
}

// UsersExist checks if any user exists.
func UsersExist() (bool, error) {
	exist := false
	err := userDB.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("Users"))
		c := b.Cursor()
		k, _ := c.First()
		if k != nil {
			exist = true
		}
		return nil
	})
	return exist, err
}

// CreateSuperUser creates a super user with full access.
func CreateSuperUser(accessKey, secret string) error {
	encrypted, err := Encrypt(secret)
	if err != nil {
		return err
	}
	user := &User{
		AccessKeyID:     accessKey,
		EncryptedSecret: encrypted,
		ACL: []ACLRule{
			{
				Effect:  "Allow",
				Buckets: []string{"*"},
				Actions: []string{"*"},
			},
		},
	}
	return CreateUser(user)
}

// CreateUserWithACL generates a new user with the given ACL rules.
func CreateUserWithACL(aclRules []ACLRule) (*User, error) {
	accessKeyID := util.GenerateRandomString(20, util.AccessKeyCharset)
	secretAccessKey := util.GenerateRandomString(40, util.SecretAccessKeyCharset)
	encrypted, err := Encrypt(secretAccessKey)
	if err != nil {
		return nil, err
	}
	newUser := &User{
		AccessKeyID:     accessKeyID,
		EncryptedSecret: encrypted,
		ACL:             aclRules,
		SecretAccessKey: secretAccessKey,
	}
	if err := CreateUser(newUser); err != nil {
		return nil, err
	}
	return newUser, nil
}

// UpdateUserACL updates the ACL for a user.
func UpdateUserACL(accessKeyID string, aclRules []ACLRule) error {
	return userDB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("Users"))
		v := b.Get([]byte(accessKeyID))
		if v == nil {
			return fmt.Errorf("user not found")
		}
		var u User
		if err := json.Unmarshal(v, &u); err != nil {
			return err
		}
		u.ACL = aclRules
		data, err := json.Marshal(u)
		if err != nil {
			return err
		}
		return b.Put([]byte(accessKeyID), data)
	})
}

// DeleteUser deletes a user from the "Users" bucket by their accessKeyID.
func DeleteUser(accessKeyID string) error {
	return userDB.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("Users"))
		return b.Delete([]byte(accessKeyID))
	})
}

// ObjectMetadata represents object metadata (e.g., ACL).
type ObjectMetadata struct {
	ACL string `json:"acl"`
}
