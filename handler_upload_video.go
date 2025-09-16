package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
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

	// Reopen temp file for S3 upload
	tempFileForUpload, err := os.Open(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to reopen temp file for upload", err)
		return
	}
	defer tempFileForUpload.Close()

	// Generate random 32-byte hex filename for S3 key
	var rnd [32]byte
	if _, err := io.ReadFull(rand.Reader, rnd[:]); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to generate random filename", err)
		return
	}
	hexName := fmt.Sprintf("%x.mp4", rnd)
	s3Key := hexName

	// Upload to S3
	putInput := &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &s3Key,
		Body:        tempFileForUpload,
		ContentType: &mediaType,
	}
	_, err = cfg.s3Client.PutObject(context.Background(), putInput)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to upload video to S3", err)
		return
	}

	// Set public S3 URL
	publicURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, s3Key)
	video.VideoURL = &publicURL

	if err := cfg.db.UpdateVideo(video); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to update video URL", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}
