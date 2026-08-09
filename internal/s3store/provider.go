package s3store

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/baicie/asteria-drive/internal/drive"
)

type Options struct {
	Endpoint              string
	PublicEndpoint        string
	Region                string
	Bucket                string
	AccessKey             string
	SecretKey             string
	UsePathStyle          bool
	AutoCreateBucket      bool
	EnableChecksumHeaders bool
}

type Provider struct {
	client          *s3.Client
	presigner       *s3.PresignClient
	bucket          string
	checksumHeaders bool
}

func New(ctx context.Context, options Options) (*Provider, error) {
	if options.Region == "" || options.Bucket == "" {
		return nil, drive.E(drive.CodeInvalidRequest, "S3 region and bucket are required")
	}
	loadOptions := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(options.Region)}
	if options.Endpoint != "" {
		// Older S3-compatible servers reject the SDK's optional X-Amz-Checksum-Mode
		// query parameter on presigned GET requests. Required checksum validation
		// remains enabled, while AWS S3 keeps the SDK default.
		loadOptions = append(loadOptions, awsconfig.WithResponseChecksumValidation(aws.ResponseChecksumValidationWhenRequired))
	}
	if options.AccessKey != "" || options.SecretKey != "" {
		if options.AccessKey == "" || options.SecretKey == "" {
			return nil, drive.E(drive.CodeInvalidRequest, "both S3 access key and secret key are required")
		}
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(options.AccessKey, options.SecretKey, ""),
		))
	}
	configuration, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("load S3 configuration: %w", err)
	}
	endpoint := strings.TrimRight(options.Endpoint, "/")
	publicEndpoint := strings.TrimRight(options.PublicEndpoint, "/")
	if publicEndpoint == "" {
		publicEndpoint = endpoint
	}
	client := newClient(configuration, endpoint, options.UsePathStyle)
	presignClient := client
	if publicEndpoint != endpoint {
		presignClient = newClient(configuration, publicEndpoint, options.UsePathStyle)
	}
	provider := &Provider{
		client: client, presigner: s3.NewPresignClient(presignClient), bucket: options.Bucket,
		checksumHeaders: options.Endpoint == "" || options.EnableChecksumHeaders,
	}
	if options.AutoCreateBucket {
		if err := provider.ensureBucket(ctx, options.Region); err != nil {
			return nil, err
		}
	}
	return provider, nil
}

func newClient(configuration aws.Config, endpoint string, usePathStyle bool) *s3.Client {
	return s3.NewFromConfig(configuration, func(s3Options *s3.Options) {
		if endpoint != "" {
			s3Options.BaseEndpoint = aws.String(endpoint)
		}
		s3Options.UsePathStyle = usePathStyle
	})
}

func (p *Provider) Bucket() string { return p.bucket }

func (p *Provider) Ready(ctx context.Context) error {
	_, err := p.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(p.bucket)})
	return mapError(err, "S3 bucket is unavailable")
}

func (p *Provider) CreateMultipart(ctx context.Context, key, mimeType string, checksum drive.Checksum) (string, error) {
	input := &s3.CreateMultipartUploadInput{
		Bucket: aws.String(p.bucket), Key: aws.String(key), ContentType: aws.String(mimeType),
	}
	if p.checksumHeaders && checksum.Algorithm == "sha256" {
		input.ChecksumAlgorithm = types.ChecksumAlgorithmSha256
	}
	output, err := p.client.CreateMultipartUpload(ctx, input)
	if err != nil {
		return "", mapError(err, "could not create multipart upload")
	}
	if output.UploadId == nil || *output.UploadId == "" {
		return "", drive.E(drive.CodeDependencyUnavailable, "object storage returned no upload id")
	}
	return *output.UploadId, nil
}

func (p *Provider) SignUploadPart(ctx context.Context, key, uploadID string, partNumber int, checksum drive.Checksum, ttl time.Duration) (drive.SignedPart, error) {
	input := &s3.UploadPartInput{
		Bucket: aws.String(p.bucket), Key: aws.String(key), UploadId: aws.String(uploadID), PartNumber: aws.Int32(int32(partNumber)),
	}
	if p.checksumHeaders && checksum.Algorithm == "sha256" {
		input.ChecksumSHA256 = aws.String(checksum.Value)
	}
	output, err := p.presigner.PresignUploadPart(ctx, input, func(options *s3.PresignOptions) {
		options.Expires = ttl
	})
	if err != nil {
		return drive.SignedPart{}, mapError(err, "could not sign upload part")
	}
	return drive.SignedPart{
		PartNumber: partNumber, Method: output.Method, URL: output.URL,
		RequiredHeaders: flattenHeaders(output.SignedHeader), ExpiresAt: time.Now().UTC().Add(ttl),
	}, nil
}

func (p *Provider) CompleteMultipart(ctx context.Context, key, uploadID string, parts []drive.CompletedPart) (drive.ObjectInfo, error) {
	completed := make([]types.CompletedPart, len(parts))
	for i, part := range parts {
		completed[i] = types.CompletedPart{PartNumber: aws.Int32(int32(part.PartNumber)), ETag: aws.String(part.ETag)}
		if p.checksumHeaders && part.Checksum.Algorithm == "sha256" {
			completed[i].ChecksumSHA256 = aws.String(part.Checksum.Value)
		}
	}
	_, err := p.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(p.bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	if err != nil {
		return drive.ObjectInfo{}, mapError(err, "could not complete multipart upload")
	}
	return p.StatObject(ctx, key)
}

func (p *Provider) AbortMultipart(ctx context.Context, key, uploadID string) error {
	_, err := p.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(p.bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
	})
	if isNotFound(err) {
		return nil
	}
	return mapError(err, "could not abort multipart upload")
}

func (p *Provider) StatObject(ctx context.Context, key string) (drive.ObjectInfo, error) {
	output, err := p.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(p.bucket), Key: aws.String(key)})
	if err != nil {
		return drive.ObjectInfo{}, mapError(err, "could not stat object")
	}
	info := drive.ObjectInfo{
		Bucket: p.bucket, ObjectKey: key, Size: aws.ToInt64(output.ContentLength), ETag: aws.ToString(output.ETag),
		ChecksumStatus: drive.ChecksumUnavailable,
	}
	if output.ChecksumSHA256 != nil && *output.ChecksumSHA256 != "" {
		info.Checksum = drive.Checksum{Algorithm: "sha256", Value: *output.ChecksumSHA256}
		info.ChecksumStatus = drive.ChecksumVerified
	}
	return info, nil
}

func (p *Provider) SignDownload(ctx context.Context, key, filename string, ttl time.Duration) (drive.SignedDownload, error) {
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
	output, err := p.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(p.bucket), Key: aws.String(key), ResponseContentDisposition: aws.String(disposition),
	}, func(options *s3.PresignOptions) {
		options.Expires = ttl
	})
	if err != nil {
		return drive.SignedDownload{}, mapError(err, "could not sign download")
	}
	return drive.SignedDownload{Method: output.Method, URL: output.URL, ExpiresAt: time.Now().UTC().Add(ttl)}, nil
}

func (p *Provider) DeleteObject(ctx context.Context, key string) error {
	_, err := p.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(p.bucket), Key: aws.String(key)})
	return mapError(err, "could not delete object")
}

func (p *Provider) ensureBucket(ctx context.Context, region string) error {
	if _, err := p.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(p.bucket)}); err == nil {
		return nil
	} else if !isBucketNotFound(err) {
		return mapError(err, "could not inspect S3 bucket")
	}
	input := &s3.CreateBucketInput{Bucket: aws.String(p.bucket)}
	if region != "us-east-1" {
		input.CreateBucketConfiguration = &types.CreateBucketConfiguration{LocationConstraint: types.BucketLocationConstraint(region)}
	}
	_, err := p.client.CreateBucket(ctx, input)
	return mapError(err, "could not create S3 bucket")
}

func flattenHeaders(headers http.Header) map[string]string {
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		if len(values) > 0 {
			result[key] = values[0]
		}
	}
	return result
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchKey", "NoSuchUpload", "404":
			return true
		}
	}
	return false
}

func isBucketNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchBucket", "404":
			return true
		}
	}
	return false
}

func mapError(err error, message string) error {
	if err == nil {
		return nil
	}
	if drive.CodeOf(err) != drive.CodeInternal {
		return err
	}
	if isNotFound(err) {
		return drive.E(drive.CodeNotFound, "object storage resource was not found")
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "InvalidPart", "InvalidPartOrder", "EntityTooSmall", "BadDigest", "InvalidRequest":
			return drive.E(drive.CodeInvalidRequest, "object storage rejected the multipart request", err)
		}
	}
	return drive.Retryable(drive.CodeDependencyUnavailable, message, err)
}

var _ drive.StorageProvider = (*Provider)(nil)
