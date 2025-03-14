package main

import (
	"encoding/base64"
	"log"
	"os"
	"sync"

	"ByteBucket/internal/router"
	"ByteBucket/internal/storage"
)

func main() {
	// Load the encryption key from the environment.
	encKeyStr := os.Getenv("ENCRYPTION_KEY")
	if encKeyStr == "" {
		log.Fatal("ENCRYPTION_KEY must be provided")
	}

	var encKey []byte
	// If the key is exactly 32 characters, assume it's a raw key.
	// Otherwise, assume it's base64-encoded.
	if len(encKeyStr) == 32 {
		encKey = []byte(encKeyStr)
	} else {
		var err error
		encKey, err = base64.StdEncoding.DecodeString(encKeyStr)
		if err != nil {
			log.Fatalf("Failed to decode ENCRYPTION_KEY as base64: %v", err)
		}
	}

	if len(encKey) != 32 {
		log.Fatalf("ENCRYPTION_KEY must be 32 bytes long after processing, got %d bytes", len(encKey))
	}

	storage.SetEncryptionKey(encKey)

	// Initialize BoltDB for file metadata.
	if err := storage.InitMetaStore("/data/meta.db"); err != nil {
		log.Fatalf("failed to initialize metadata store: %v", err)
	}

	// Initialize BoltDB for users.
	if err := storage.InitUserStore("/data/users.db"); err != nil {
		log.Fatalf("Failed to initialize user store: %v", err)
	}

	// Create super-user if needed.
	exist, err := storage.UsersExist()
	if err != nil {
		log.Fatalf("failed to check users: %v", err)
	}
	envAccessKey := os.Getenv("ACCESS_KEY_ID")
	envSecret := os.Getenv("SECRET_ACCESS_KEY")
	if !exist {
		if envAccessKey == "" || envSecret == "" {
			log.Fatal("No users in DB and ACCESS_KEY_ID/SECRET_ACCESS_KEY not provided")
		}
		if err := storage.CreateSuperUser(envAccessKey, envSecret); err != nil {
			log.Fatalf("failed to create super user: %v", err)
		}
		log.Println("Super user created from environment variables")
	} else {
		log.Println("User database already initialized; environment credentials discarded")
	}

	// Create both the storage router and admin router.
	storageRouter := router.NewStorageRouter()
	adminRouter := router.NewAdminRouter()

	// Start both servers concurrently.
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		log.Println("Storage server listening on port 9000")
		if err := storageRouter.Run(":9000"); err != nil {
			log.Fatalf("Storage server failed: %v", err)
		}
	}()

	go func() {
		defer wg.Done()
		log.Println("Admin server listening on port 9001")
		if err := adminRouter.Run(":9001"); err != nil {
			log.Fatalf("Admin server failed: %v", err)
		}
	}()

	wg.Wait()
}
