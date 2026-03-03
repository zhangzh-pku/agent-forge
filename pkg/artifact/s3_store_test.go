package artifact

import (
	"strings"
	"testing"

	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func TestBuildPutObjectInputUsesSSEKMS(t *testing.T) {
	store := &S3Store{
		bucket:      "bucket",
		sseKMSKeyID: "",
	}

	input := store.buildPutObjectInput("k", strings.NewReader("data"))
	if input.ServerSideEncryption != s3types.ServerSideEncryptionAwsKms {
		t.Fatalf("expected aws:kms SSE, got %v", input.ServerSideEncryption)
	}
	if input.SSEKMSKeyId != nil {
		t.Fatalf("expected SSEKMSKeyId nil when key not configured, got %v", *input.SSEKMSKeyId)
	}
}

func TestBuildPutObjectInputUsesConfiguredKMSKey(t *testing.T) {
	store := &S3Store{
		bucket:      "bucket",
		sseKMSKeyID: "arn:aws:kms:us-east-1:123456789012:key/test",
	}

	input := store.buildPutObjectInput("k", strings.NewReader("data"))
	if input.SSEKMSKeyId == nil || *input.SSEKMSKeyId != store.sseKMSKeyID {
		t.Fatalf("expected SSEKMSKeyId=%q, got %+v", store.sseKMSKeyID, input.SSEKMSKeyId)
	}
}
