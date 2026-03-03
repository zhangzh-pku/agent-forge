package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

const defaultS3PresignExpiry = 15 * time.Minute

// S3StoreConfig defines S3 artifact store settings.
type S3StoreConfig struct {
	Bucket         string
	PresignExpires time.Duration
}

// S3Store stores artifacts in Amazon S3.
type S3Store struct {
	client         *s3.Client
	presigner      *s3.PresignClient
	bucket         string
	presignExpires time.Duration
}

// NewS3Store creates a production artifact store backed by S3.
func NewS3Store(client *s3.Client, cfg S3StoreConfig) (*S3Store, error) {
	if client == nil {
		return nil, fmt.Errorf("artifact: s3 client is required")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("artifact: bucket is required")
	}
	if cfg.PresignExpires <= 0 {
		cfg.PresignExpires = defaultS3PresignExpiry
	}

	return &S3Store{
		client:         client,
		presigner:      s3.NewPresignClient(client),
		bucket:         cfg.Bucket,
		presignExpires: cfg.PresignExpires,
	}, nil
}

// Put writes data to S3 and returns sha256 + size metadata.
func (s *S3Store) Put(ctx context.Context, key string, r io.Reader) (string, int64, error) {
	if key == "" {
		return "", 0, fmt.Errorf("artifact: key is required")
	}
	if r == nil {
		return "", 0, fmt.Errorf("artifact: reader is nil")
	}

	h := sha256.New()
	counter := &hashCounter{h: h}
	body := io.TeeReader(r, counter)

	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   body,
	})
	if err != nil {
		return "", 0, fmt.Errorf("artifact: s3 put object: %w", err)
	}

	return hex.EncodeToString(h.Sum(nil)), counter.n, nil
}

// Get reads an artifact from S3.
func (s *S3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if key == "" {
		return nil, fmt.Errorf("artifact: key is required")
	}

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isS3NotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("artifact: s3 get object: %w", err)
	}
	return out.Body, nil
}

// Exists checks if an artifact key exists.
func (s *S3Store) Exists(ctx context.Context, key string) (bool, error) {
	if key == "" {
		return false, fmt.Errorf("artifact: key is required")
	}

	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isS3NotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("artifact: s3 head object: %w", err)
	}
	return true, nil
}

// PresignedURL generates a temporary download URL.
func (s *S3Store) PresignedURL(ctx context.Context, key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("artifact: key is required")
	}

	out, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = s.presignExpires
	})
	if err != nil {
		if isS3NotFound(err) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("artifact: s3 presign get object: %w", err)
	}
	return out.URL, nil
}

type hashCounter struct {
	h hash.Hash
	n int64
}

func (c *hashCounter) Write(p []byte) (int, error) {
	n, err := c.h.Write(p)
	c.n += int64(n)
	return n, err
}

func isS3NotFound(err error) bool {
	if err == nil {
		return false
	}
	var noSuchKey *s3types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		if code == "NotFound" || code == "NoSuchKey" {
			return true
		}
	}
	return false
}
