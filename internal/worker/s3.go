// Copyright 2026 Kushan J
// SPDX-License-Identifier: Apache-2.0

package worker

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

// S3Uploader handles real uploads to AWS S3.
type S3Uploader struct {
	client *s3.Client
	bucket string
	region string
}

// NewS3Uploader creates an S3 client using default AWS credential chain
// (env vars AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY, or ~/.aws/credentials, or IAM role).
func NewS3Uploader(region, bucket string) (*S3Uploader, error) {
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg)

	return &S3Uploader{
		client: client,
		bucket: bucket,
		region: region,
	}, nil
}

// UploadVideo uploads a local video file to S3 with a deterministic key
// based on the job ID (guarantees idempotency on retry).
// Returns a presigned GET URL valid for 7 days.
func (u *S3Uploader) UploadVideo(ctx context.Context, jobID uuid.UUID, localPath string) (string, error) {
	s3Key := fmt.Sprintf("videos/%s.mp4", jobID.String())

	file, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("failed to open video file: %w", err)
	}
	defer file.Close()

	log.Info().
		Str("job_id", jobID.String()).
		Str("bucket", u.bucket).
		Str("key", s3Key).
		Msg("Uploading video to S3")

	_, err = u.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(u.bucket),
		Key:         aws.String(s3Key),
		Body:        file,
		ContentType: aws.String("video/mp4"),
	})
	if err != nil {
		return "", fmt.Errorf("S3 PutObject failed: %w", err)
	}

	// Generate presigned GET URL (valid for 7 days)
	presigner := s3.NewPresignClient(u.client)
	presignedReq, err := presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket:                     aws.String(u.bucket),
		Key:                        aws.String(s3Key),
		ResponseContentDisposition: aws.String("attachment; filename=\"geostream_video.mp4\""),
	}, s3.WithPresignExpires(7*24*time.Hour))
	if err != nil {
		return "", fmt.Errorf("failed to presign URL: %w", err)
	}

	log.Info().
		Str("job_id", jobID.String()).
		Str("url", presignedReq.URL[:80]+"...").
		Msg("S3 upload complete, presigned URL generated")

	return presignedReq.URL, nil
}

// DeleteVideo removes the video file from S3 to save storage costs.
func (u *S3Uploader) DeleteVideo(ctx context.Context, jobID uuid.UUID) error {
	s3Key := fmt.Sprintf("videos/%s.mp4", jobID.String())

	_, err := u.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(u.bucket),
		Key:    aws.String(s3Key),
	})
	if err != nil {
		return fmt.Errorf("S3 DeleteObject failed: %w", err)
	}

	log.Info().
		Str("job_id", jobID.String()).
		Str("bucket", u.bucket).
		Str("key", s3Key).
		Msg("Deleted video from S3")

	return nil
}
