package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestS3StoreIntegration_PutGetExistsAndPresignedURL(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv("AGENTFORGE_INTEGRATION_AWS_ENDPOINT"))
	if endpoint == "" {
		t.Skip("set AGENTFORGE_INTEGRATION_AWS_ENDPOINT to run S3 integration tests")
	}

	ctx := context.Background()
	client := integrationS3Client(t, endpoint)
	bucket := fmt.Sprintf("af-artifact-it-%d", time.Now().UnixNano())
	createIntegrationBucket(t, ctx, client, bucket)

	store, err := NewS3Store(client, S3StoreConfig{
		Bucket:         bucket,
		PresignExpires: 2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("new s3 store: %v", err)
	}

	key := "artifacts/it.txt"
	payload := []byte("hello artifact integration")
	sum := sha256.Sum256(payload)
	wantSHA := hex.EncodeToString(sum[:])

	sha, size, err := store.Put(ctx, key, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("put artifact: %v", err)
	}
	if sha != wantSHA {
		t.Fatalf("unexpected sha256: got=%s want=%s", sha, wantSHA)
	}
	if size != int64(len(payload)) {
		t.Fatalf("unexpected size: got=%d want=%d", size, len(payload))
	}

	exists, err := store.Exists(ctx, key)
	if err != nil {
		t.Fatalf("exists check: %v", err)
	}
	if !exists {
		t.Fatal("expected key to exist after put")
	}

	rc, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("get artifact: %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("unexpected payload: got=%q want=%q", got, payload)
	}

	url, err := store.PresignedURL(ctx, key)
	if err != nil {
		t.Fatalf("presign url: %v", err)
	}
	if strings.TrimSpace(url) == "" {
		t.Fatal("expected non-empty presigned url")
	}

	missingKey := "artifacts/missing.txt"
	exists, err = store.Exists(ctx, missingKey)
	if err != nil {
		t.Fatalf("exists missing key: %v", err)
	}
	if exists {
		t.Fatal("expected missing key to not exist")
	}

	_, err = store.Get(ctx, missingKey)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from get missing key, got %v", err)
	}

	_, err = store.PresignedURL(ctx, missingKey)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from presign missing key, got %v", err)
	}
}

func integrationS3Client(t *testing.T, endpoint string) *s3.Client {
	t.Helper()
	cfg, err := awscfg.LoadDefaultConfig(
		context.Background(),
		awscfg.WithRegion("us-east-1"),
		awscfg.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test", "test", "")),
	)
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = &endpoint
		o.UsePathStyle = true
	})
}

func createIntegrationBucket(t *testing.T, ctx context.Context, client *s3.Client, bucket string) {
	t.Helper()
	_, err := client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: &bucket,
	})
	if err != nil {
		t.Fatalf("create bucket %s: %v", bucket, err)
	}

	deadline := time.Now().Add(20 * time.Second)
	for {
		_, headErr := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: &bucket})
		if headErr == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("bucket %s not ready: %v", bucket, headErr)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
