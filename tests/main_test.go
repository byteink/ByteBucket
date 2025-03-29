package tests

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Global variables
var storageURL string
var adminURL string

// Admin credentials
var adminCreds = struct {
	AccessKeyID     string
	SecretAccessKey string
	EncryptionKey   string
}{
	AccessKeyID:     "APE6at7CMFvJaEJjnmbC",
	SecretAccessKey: "40ylGQ3lRaxE/SQFRZrHZY+e+XD7CBMVa8ioUsAO",
	EncryptionKey:   "zcY9EnJ5gVVDyfrNlXsuEOToLwC7cWsiz02xGKbBo1g=",
}

// TODO: support binary builds
// Builds the binary before running tests
//func buildBinary() (string, error) {
//	binaryPath := "../build/bytebucket" // Adjust path as needed
//	cmd := exec.Command("go", "build", "-o", binaryPath, "../cmd/bytebucket")
//	cmd.Stdout = os.Stdout
//	cmd.Stderr = os.Stderr
//	if err := cmd.Run(); err != nil {
//		return "", fmt.Errorf("failed to build binary: %v", err)
//	}
//	return binaryPath, nil
//}
//
// Starts the binary and returns the process
//func startBinary(binaryPath string) (*exec.Cmd, error) {
//	cmd := exec.Command(binaryPath)
//
//	// Ensure environment variables are passed to the binary
//	cmd.Env = append(os.Environ(),
//		"GIN_MODE=release",
//		"ACCESS_KEY_ID="+adminCreds.AccessKeyID,
//		"SECRET_ACCESS_KEY="+adminCreds.SecretAccessKey,
//		"ENCRYPTION_KEY="+adminCreds.EncryptionKey,
//	)
//
//	cmd.Stdout = os.Stdout
//	cmd.Stderr = os.Stderr
//
//	if err := cmd.Start(); err != nil {
//		return nil, fmt.Errorf("failed to start binary: %v", err)
//	}
//
//	// Allow time for the binary to initialize
//	time.Sleep(5 * time.Second)
//	return cmd, nil
//}

func TestMain(m *testing.M) {
	ctx := context.Background()

	// === RUN DOCKER TEST ===
	fmt.Println("Starting Docker-Based Test...")

	// Try building the container directly
	req := testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{
			Context:    "..",                  // Root project directory
			Dockerfile: "./docker/Dockerfile", // Path to the Dockerfile
		},
		ExposedPorts: []string{"9000/tcp", "9001/tcp"},
		WaitingFor:   wait.ForLog("Server started successfully").WithStartupTimeout(30 * time.Second),
		Env: map[string]string{
			"ENCRYPTION_KEY":    adminCreds.EncryptionKey,
			"ACCESS_KEY_ID":     adminCreds.AccessKeyID,
			"SECRET_ACCESS_KEY": adminCreds.SecretAccessKey,
		},
		Mounts: testcontainers.Mounts(testcontainers.VolumeMount("bytebucket", "/data")),
	}

	// Build and start container
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})

	// If building from Dockerfile fails, try using a pre-built image
	if err != nil {
		fmt.Printf("Failed to build from Dockerfile: %v\n", err)
		fmt.Println("Attempting to build the Docker image manually...")

		// Try to build the image manually
		cmd := exec.Command("docker", "build", "-f", "../docker/Dockerfile", "-t", "bytebucket-test", "..")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		if err := cmd.Run(); err != nil {
			fmt.Printf("Manual Docker build failed: %v\n", err)
			fmt.Println("Please run the following commands manually before running tests:")
			fmt.Println("  cd .. && docker build -f docker/Dockerfile -t bytebucket-test .")
			os.Exit(1)
		}

		// Use the manually built image
		reqWithImage := testcontainers.ContainerRequest{
			Image:        "bytebucket-test",
			ExposedPorts: []string{"9000/tcp", "9001/tcp"},
			WaitingFor:   wait.ForLog("Server started successfully").WithStartupTimeout(30 * time.Second),
			Env: map[string]string{
				"ENCRYPTION_KEY":    adminCreds.EncryptionKey,
				"ACCESS_KEY_ID":     adminCreds.AccessKeyID,
				"SECRET_ACCESS_KEY": adminCreds.SecretAccessKey,
			},
			Mounts: testcontainers.Mounts(testcontainers.VolumeMount("bytebucket", "/data")),
		}

		container, err = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: reqWithImage,
			Started:          true,
		})

		if err != nil {
			panic(fmt.Sprintf("Failed to start container from pre-built image: %v", err))
		}
	}

	// Retrieve mapped ports
	mappedStoragePort, err := container.MappedPort(ctx, "9000")
	if err != nil {
		panic(fmt.Sprintf("Failed to get mapped storage port: %v", err))
	}
	mappedAdminPort, err := container.MappedPort(ctx, "9001")
	if err != nil {
		panic(fmt.Sprintf("Failed to get mapped admin port: %v", err))
	}

	// Assign dynamic URLs
	storageURL = fmt.Sprintf("http://localhost:%s", mappedStoragePort.Port())
	adminURL = fmt.Sprintf("http://localhost:%s", mappedAdminPort.Port())

	fmt.Printf("ByteBucket container started:\n")
	fmt.Printf("Storage API: %s\n", storageURL)
	fmt.Printf("Admin API: %s\n", adminURL)

	// Run tests against the Docker container
	exitCode := m.Run()

	// Cleanup: Stop container after tests
	if err := container.Terminate(ctx); err != nil {
		fmt.Printf("Failed to stop ByteBucket container: %v\n", err)
	}

	// TODO: support binary run
	// === RUN BINARY TEST ===
	//fmt.Println("Starting Binary Build Test...")
	//binaryPath, err := buildBinary()
	//if err != nil {
	//	fmt.Printf("Binary build failed: %v\n", err)
	//	os.Exit(1)
	//}
	//
	//binaryCmd, err := startBinary(binaryPath)
	//if err != nil {
	//	fmt.Printf("Binary start failed: %v\n", err)
	//	os.Exit(1)
	//}
	//
	//// Assume the binary runs on default ports
	//storageURL = "http://localhost:9000"
	//adminURL = "http://localhost:9001"
	//
	//fmt.Printf("Binary ByteBucket started at Storage: %s, Admin: %s\n", storageURL, adminURL)
	//
	//// Run tests against the binary
	//exitCode = m.Run()
	//
	//// Stop the binary
	//err = binaryCmd.Process.Kill()
	//if err != nil {
	//	fmt.Printf("Failed to kill binary: %v\n", err)
	//}

	os.Exit(exitCode)
}
