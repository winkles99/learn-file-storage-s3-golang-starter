package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/database"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	// Get video metadata and check ownership
	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error retrieving video", err)
		return
	}
	if video.ID == uuid.Nil {
		respondWithError(w, http.StatusNotFound, "Video not found", nil)
		return
	}
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "You do not own this video", nil)
		return
	}

	// Limit upload size to 1GB
	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)

	// Parse multipart form (use 32MB memory for large files)
	const maxMemory = int64(32 << 20) // 32 MB
	if err := r.ParseMultipartForm(maxMemory); err != nil {
		respondWithError(w, http.StatusBadRequest, "Error parsing form data", err)
		return
	}

	file, fileHeader, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Missing or invalid 'video' file", err)
		return
	}
	defer file.Close()

	ct := fileHeader.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil || mediaType == "" {
		respondWithError(w, http.StatusBadRequest, "Invalid Content-Type header", err)
		return
	}
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Unsupported media type; only video/mp4 allowed", nil)
		return
	}

	// Save to temp file
	tempFile, err := os.CreateTemp("", "tubely-upload-*.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to create temp file", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	if _, err := io.Copy(tempFile, file); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to save video to temp file", err)
		return
	}

	// Reset file pointer to beginning
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to seek temp file", err)
		return
	}

	// Process file for fast start (move moov atom) and open processed file for upload
	processedPath, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to process video for fast start", err)
		return
	}
	defer os.Remove(processedPath)

	processedFile, err := os.Open(processedPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to open processed file for upload", err)
		return
	}
	defer processedFile.Close()

	// Generate random 32-byte hex filename for S3 key
	var rnd [32]byte
	if _, err := io.ReadFull(rand.Reader, rnd[:]); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to generate random filename", err)
		return
	}
	// Determine aspect ratio of the saved temp file and choose prefix
	aspect, err := getVideoAspectRatio(tempFile.Name())
	prefix := "other"
	if err == nil {
		if aspect == "16:9" {
			prefix = "landscape"
		} else if aspect == "9:16" {
			prefix = "portrait"
		}
	}
	s3Key := fmt.Sprintf("%s/%x.mp4", prefix, rnd)

	// Upload to S3
	putInput := &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &s3Key,
		Body:        processedFile,
		ContentType: &mediaType,
	}
	_, err = cfg.s3Client.PutObject(context.Background(), putInput)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to upload video to S3", err)
		return
	}

	// Store "bucket,key" in video_url (e.g., tubely-private-53827,portrait/abc.mp4)
	commaDelimited := fmt.Sprintf("%s,%s", cfg.s3Bucket, s3Key)
	video.VideoURL = &commaDelimited

	if err := cfg.db.UpdateVideo(video); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to update video URL", err)
		return
	}

	// Sign before returning to the client
	signedVideo, err := cfg.dbVideoToSignedVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to generate presigned URL", err)
		return
	}

	respondWithJSON(w, http.StatusOK, signedVideo)
}

// dbVideoToSignedVideo reads stored "bucket,key", generates a presigned URL, and returns the updated video.
func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
	if video.VideoURL == nil || strings.TrimSpace(*video.VideoURL) == "" {
		return video, fmt.Errorf("video has no URL to sign")
	}

	parts := strings.SplitN(*video.VideoURL, ",", 2)
	if len(parts) != 2 {
		return video, fmt.Errorf("invalid video URL format: expected 'bucket,key'")
	}
	bucket := strings.TrimSpace(parts[0])
	key := strings.TrimSpace(parts[1])
	if bucket == "" || key == "" {
		return video, fmt.Errorf("invalid bucket or key in video URL")
	}

	signedURL, err := generatePresignedURL(cfg.s3Client, bucket, key, 15*time.Minute)
	if err != nil {
		return video, err
	}
	video.VideoURL = &signedURL
	return video, nil
}

// generatePresignedURL creates a presigned GET URL for the object.
func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
	presigner := s3.NewPresignClient(s3Client)
	out, err := presigner.PresignGetObject(
		context.Background(),
		&s3.GetObjectInput{
			Bucket: aws.String(bucket),
			Key:    aws.String(key),
		},
		s3.WithPresignExpires(expireTime),
	)
	if err != nil {
		return "", err
	}
	return out.URL, nil
}
