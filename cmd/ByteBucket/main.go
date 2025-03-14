package main

import (
	"log"
	"os"

	"ByteBucket/internal/router"
	"ByteBucket/internal/storage"
)

func main() {
	// Load the encryption key (must be 32 bytes for AES-256).
	encKey := os.Getenv("ENCRYPTION_KEY")
	if encKey == "" || len(encKey) != 32 {
		log.Fatal("ENCRYPTION_KEY must be provided and be 32 bytes long for AES-256")
	}
	storage.SetEncryptionKey([]byte(encKey))

	// Initialize BoltDB at /data/meta.db.
	if err := storage.InitMetaStore("/data/meta.db"); err != nil {
		log.Fatalf("failed to initialize metadata store: %v", err)
	}

	// If no users exist, create a super-user from environment variables.
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

	// Create the HTTP router.
	r := router.NewRouter()

	log.Println("Server listening on port 9000")
	if err := r.Run(":9000"); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
