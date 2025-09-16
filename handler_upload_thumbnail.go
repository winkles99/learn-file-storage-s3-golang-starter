package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
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

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	// Parse the multipart form with a 10MB memory limit
	const maxMemory = int64(10 << 20) // 10 MB
	if err := r.ParseMultipartForm(maxMemory); err != nil {
		respondWithError(w, http.StatusBadRequest, "Error parsing form data", err)
		return
	}

	// Get the file and header from the form using key "thumbnail"
	file, fileHeader, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Missing or invalid 'thumbnail' file", err)
		return
	}
	defer file.Close()

	// Extract and parse media type from Content-Type header
	ct := fileHeader.Header.Get("Content-Type")
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil || mediaType == "" {
		respondWithError(w, http.StatusBadRequest, "Invalid Content-Type header", err)
		return
	}
	if mediaType != "image/jpeg" && mediaType != "image/png" {
		respondWithError(w, http.StatusBadRequest, "Unsupported media type; only image/jpeg and image/png are allowed", nil)
		return
	}

	// Determine a file extension from the Content-Type header
	var ext string
	if exts, err := mime.ExtensionsByType(mediaType); err == nil && len(exts) > 0 {
		ext = exts[0]
	}
	// Preference/fallbacks for common images
	switch mediaType {
	case "image/jpeg":
		ext = ".jpg" // prefer .jpg over .jpeg
	case "image/svg+xml":
		if ext == "" {
			ext = ".svg"
		}
	}

	// Light log for visibility
	fmt.Printf("received thumbnail: mediaType=%s ext=%s\n", mediaType, ext)

	// Get the video metadata and ensure the authenticated user owns it
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

	// Create a unique file path using the video ID and save the thumbnail to disk
	if ext == "" {
		ext = ".img" // fallback extension if none detected
	}

	// Create a random 32-byte filename and encode as URL-safe base64 (no padding)
	var rnd [32]byte // cryptographically secure random bytes
	if _, err := rand.Read(rnd[:]); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to generate random filename", err)
		return
	}
	randomName := base64.RawURLEncoding.EncodeToString(rnd[:])
	filename := randomName + ext
	fullPath := filepath.Join(cfg.assetsRoot, filename)

	out, err := os.Create(fullPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to create thumbnail file", err)
		return
	}
	defer out.Close()

	// Stream copy the uploaded file directly to disk
	if _, err := io.Copy(out, file); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to write thumbnail to disk", err)
		return
	}

	// Set the public URL pointing to the saved asset
	publicURL := fmt.Sprintf("http://localhost:%s/assets/%s", cfg.port, filename)
	video.ThumbnailURL = &publicURL

	if err := cfg.db.UpdateVideo(video); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to update video thumbnail URL", err)
		return
	}

	// Respond with the updated video metadata
	respondWithJSON(w, http.StatusOK, video)
}
