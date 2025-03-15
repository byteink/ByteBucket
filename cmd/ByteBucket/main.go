package main

import (
	"encoding/base64"
	"log"
	"os"
	"sync"

	"ByteBucket/internal/router"
	"ByteBucket/internal/storage"
)

// ensureDirectoriesExist checks and creates required directories at startup.
func ensureDirectoriesExist() {
	requiredDirs := []string{"/data", "/data/objects"}

	for _, dir := range requiredDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			log.Printf("Directory %s not found, creating...", dir)
			if err := os.MkdirAll(dir, 0755); err != nil {
				log.Fatalf("Failed to create directory %s: %v", dir, err)
			}
		}
	}
	log.Println("Required directories are present.")
}

func main() {
	// Ensure required directories exist before anything else
	ensureDirectoriesExist()

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

	// Initialize BoltDB for users.
	if err := storage.InitUserStore("/data/users.db"); err != nil {
		log.Fatalf("Failed to initialize user store: %v", err)
	}

	// Create super-user if needed.
	exist, err := storage.UsersExist()
	if err != nil {
		log.Fatalf("Failed to check users: %v", err)
	}
	envAccessKey := os.Getenv("ACCESS_KEY_ID")
	envSecret := os.Getenv("SECRET_ACCESS_KEY")
	if !exist {
		if envAccessKey == "" || envSecret == "" {
			log.Fatal("No users in DB and ACCESS_KEY_ID/SECRET_ACCESS_KEY not provided")
		}
		if err := storage.CreateSuperUser(envAccessKey, envSecret); err != nil {
			log.Fatalf("Failed to create super user: %v", err)
		}
		log.Println("Super user created from environment variables")
	} else {
		log.Println("User database already initialized; environment credentials discarded")
	}

	// Create both the storage router and admin router.
	storageRouter := router.NewStorageRouter()
	adminRouter := router.NewAdminRouter()

	// Channel to signal when both servers are ready
	serverReady := make(chan bool, 2)

	var wg sync.WaitGroup
	wg.Add(2)

	// Start Storage Server
	go func() {
		defer wg.Done()
		log.Println("Storage server listening on port 9000")
		serverReady <- true // Signal that this server has started
		if err := storageRouter.Run(":9000"); err != nil {
			log.Fatalf("Storage server failed: %v", err)
		}
	}()

	// Start Admin Server
	go func() {
		defer wg.Done()
		log.Println("Admin server listening on port 9001")
		serverReady <- true // Signal that this server has started
		if err := adminRouter.Run(":9001"); err != nil {
			log.Fatalf("Admin server failed: %v", err)
		}
	}()

	// Wait for both servers to signal they have started
	<-serverReady
	<-serverReady
	log.Println("Server started successfully") // Log only after both servers are up

	wg.Wait()
}
