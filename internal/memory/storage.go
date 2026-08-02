package memory

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/baicie/asteria-drive/internal/drive"
)

type multipartUpload struct {
	key      string
	mimeType string
	checksum drive.Checksum
	parts    map[int][]byte
}

type storedObject struct {
	data []byte
	info drive.ObjectInfo
}

type Storage struct {
	mu       sync.RWMutex
	bucket   string
	uploads  map[string]*multipartUpload
	objects  map[string]storedObject
	readyErr error
	baseURL  string
}

func NewStorage(bucket string) *Storage {
	if bucket == "" {
		bucket = "asteria-memory"
	}
	return &Storage{
		bucket: bucket, uploads: make(map[string]*multipartUpload), objects: make(map[string]storedObject),
		baseURL: "memory://" + bucket,
	}
}

func (s *Storage) SetReadyError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readyErr = err
}

func (s *Storage) Ready(context.Context) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readyErr
}

func (s *Storage) Bucket() string { return s.bucket }

func (s *Storage) CreateMultipart(_ context.Context, key, mimeType string, checksum drive.Checksum) (string, error) {
	id, err := drive.NewID()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uploads[id] = &multipartUpload{key: key, mimeType: mimeType, checksum: checksum, parts: make(map[int][]byte)}
	return id, nil
}

func (s *Storage) SignUploadPart(_ context.Context, key, uploadID string, partNumber int, checksum drive.Checksum, ttl time.Duration) (drive.SignedPart, error) {
	s.mu.RLock()
	upload, ok := s.uploads[uploadID]
	s.mu.RUnlock()
	if !ok || upload.key != key {
		return drive.SignedPart{}, drive.E(drive.CodeNotFound, "multipart upload was not found")
	}
	expiresAt := time.Now().UTC().Add(ttl)
	query := url.Values{"upload_id": {uploadID}, "part_number": {fmt.Sprint(partNumber)}}
	headers := map[string]string{}
	if checksum.Algorithm == "sha256" {
		headers["x-amz-checksum-sha256"] = checksum.Value
	}
	return drive.SignedPart{
		PartNumber: partNumber, Method: "PUT", URL: s.baseURL + "/" + url.PathEscape(key) + "?" + query.Encode(),
		RequiredHeaders: headers, ExpiresAt: expiresAt,
	}, nil
}

func (s *Storage) PutPart(uploadID string, partNumber int, data []byte) (drive.CompletedPart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	upload, ok := s.uploads[uploadID]
	if !ok {
		return drive.CompletedPart{}, drive.E(drive.CodeNotFound, "multipart upload was not found")
	}
	copyData := append([]byte(nil), data...)
	upload.parts[partNumber] = copyData
	hash := sha256.Sum256(copyData)
	return drive.CompletedPart{
		PartNumber: partNumber, ETag: `"` + hex.EncodeToString(hash[:16]) + `"`,
		Checksum: drive.Checksum{Algorithm: "sha256", Value: base64.StdEncoding.EncodeToString(hash[:])}, Size: int64(len(copyData)),
	}, nil
}

func (s *Storage) CompleteMultipart(_ context.Context, key, uploadID string, parts []drive.CompletedPart) (drive.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	upload, ok := s.uploads[uploadID]
	if !ok || upload.key != key {
		if object, exists := s.objects[key]; exists {
			return object.info, nil
		}
		return drive.ObjectInfo{}, drive.E(drive.CodeNotFound, "multipart upload was not found")
	}
	ordered := append([]drive.CompletedPart(nil), parts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].PartNumber < ordered[j].PartNumber })
	data := make([]byte, 0)
	for _, part := range ordered {
		stored, exists := upload.parts[part.PartNumber]
		if !exists {
			return drive.ObjectInfo{}, drive.E(drive.CodeInvalidRequest, "multipart part is missing")
		}
		hash := sha256.Sum256(stored)
		expectedETag := `"` + hex.EncodeToString(hash[:16]) + `"`
		if part.ETag != expectedETag {
			return drive.ObjectInfo{}, drive.E(drive.CodeInvalidRequest, "multipart part ETag does not match")
		}
		data = append(data, stored...)
	}
	hash := sha256.Sum256(data)
	checksum := drive.Checksum{Algorithm: "sha256", Value: base64.StdEncoding.EncodeToString(hash[:])}
	status := drive.ChecksumVerified
	if upload.checksum.Algorithm == "sha256" && upload.checksum.Value != checksum.Value {
		return drive.ObjectInfo{}, drive.E(drive.CodeInvalidRequest, "completed object checksum does not match")
	}
	info := drive.ObjectInfo{
		Bucket: s.bucket, ObjectKey: key, Size: int64(len(data)), ETag: `"` + hex.EncodeToString(hash[:16]) + `"`,
		Checksum: checksum, ChecksumStatus: status,
	}
	s.objects[key] = storedObject{data: append([]byte(nil), data...), info: info}
	delete(s.uploads, uploadID)
	return info, nil
}

func (s *Storage) AbortMultipart(_ context.Context, key, uploadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	upload, ok := s.uploads[uploadID]
	if !ok {
		return nil
	}
	if upload.key != key {
		return drive.E(drive.CodeNotFound, "multipart upload was not found")
	}
	delete(s.uploads, uploadID)
	return nil
}

func (s *Storage) StatObject(_ context.Context, key string) (drive.ObjectInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	object, ok := s.objects[key]
	if !ok {
		return drive.ObjectInfo{}, drive.E(drive.CodeNotFound, "object was not found")
	}
	return object.info, nil
}

func (s *Storage) SignDownload(_ context.Context, key, filename string, ttl time.Duration) (drive.SignedDownload, error) {
	s.mu.RLock()
	_, ok := s.objects[key]
	s.mu.RUnlock()
	if !ok {
		return drive.SignedDownload{}, drive.E(drive.CodeNotFound, "object was not found")
	}
	expiresAt := time.Now().UTC().Add(ttl)
	query := url.Values{"filename": {filename}, "expires": {expiresAt.Format(time.RFC3339)}}
	return drive.SignedDownload{Method: "GET", URL: s.baseURL + "/" + url.PathEscape(key) + "?" + query.Encode(), ExpiresAt: expiresAt}, nil
}

func (s *Storage) DeleteObject(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}

func (s *Storage) Object(key string) ([]byte, drive.ObjectInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	object, ok := s.objects[key]
	return append([]byte(nil), object.data...), object.info, ok
}

var _ drive.StorageProvider = (*Storage)(nil)
