package audiotagger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const taggedMarker = "AUDIOTAPE_TAGGED=1"

type metadata struct {
	SchemaVersion int `json:"schemaVersion"`

	Spotify struct {
		URI     string `json:"uri"`
		TrackID string `json:"trackId"`
		FileID  string `json:"fileId"`
	} `json:"spotify"`

	Track struct {
		Title       string `json:"title"`
		TrackNumber *int32 `json:"trackNumber"`
		DiscNumber  *int32 `json:"discNumber"`
		ISRC        string `json:"isrc"`
	} `json:"track"`

	Artists []struct {
		Name string `json:"name"`
		Role string `json:"role"`
	} `json:"artists"`

	Album *struct {
		Name  string `json:"name"`
		Label string `json:"label"`

		Date *struct {
			Year  *int32 `json:"year"`
			Month *int32 `json:"month"`
			Day   *int32 `json:"day"`
		} `json:"date"`

		Artists []struct {
			Name string `json:"name"`
		} `json:"artists"`
	} `json:"album"`
}

// Run scans dir for AudioTape metadata sidecars and tags the matching
// Ogg/Vorbis files. It runs until ctx is cancelled.
func Run(ctx context.Context, dir string, scanInterval time.Duration) error {
	if _, err := exec.LookPath("vorbiscomment"); err != nil {
		return fmt.Errorf("vorbiscomment not found in PATH: %w", err)
	}

	if scanInterval <= 0 {
		scanInterval = 3 * time.Second
	}

	log.Printf("AudioTape Tagger started")
	log.Printf("watch directory: %s", dir)
	log.Printf("scan interval: %s", scanInterval)

	// Run one scan immediately instead of waiting for the first ticker event.
	if err := scan(dir); err != nil {
		log.Printf("AudioTape Tagger scan error: %v", err)
	}

	ticker := time.NewTicker(scanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case <-ticker.C:
			if err := scan(dir); err != nil {
				log.Printf("AudioTape Tagger scan error: %v", err)
			}
		}
	}
}

func scan(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		if !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}

		jsonPath := filepath.Join(dir, entry.Name())

		if err := process(jsonPath); err != nil {
			log.Printf("%s: %v", entry.Name(), err)
		}
	}

	return nil
}

func process(jsonPath string) error {
	base := strings.TrimSuffix(jsonPath, filepath.Ext(jsonPath))
	oggPath := base + ".ogg"

	info, err := os.Stat(oggPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// The JSON may become visible before its matching OGG.
			return nil
		}

		return fmt.Errorf("stat ogg: %w", err)
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("ogg is not a regular file")
	}

	tagged, err := isTagged(oggPath)
	if err != nil {
		return fmt.Errorf("check tags: %w", err)
	}

	if tagged {
		return nil
	}

	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return fmt.Errorf("read metadata: %w", err)
	}

	var meta metadata

	if err := json.Unmarshal(data, &meta); err != nil {
		return fmt.Errorf("decode metadata: %w", err)
	}

	if meta.SchemaVersion != 1 {
		return fmt.Errorf(
			"unsupported metadata schema version: %d",
			meta.SchemaVersion,
		)
	}

	comments := buildComments(meta)

	if len(comments) == 0 {
		return fmt.Errorf("metadata contains no usable tags")
	}

	comments = append(comments, taggedMarker)

	if err := writeTagsAtomic(oggPath, comments, info.Mode()); err != nil {
		return fmt.Errorf("write tags: %w", err)
	}

	log.Printf(
		"tagged: %s - %s",
		artistDisplay(meta),
		meta.Track.Title,
	)

	return nil
}

func buildComments(meta metadata) []string {
	var tags []string

	add := func(key, value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			tags = append(tags, key+"="+value)
		}
	}

	add("TITLE", meta.Track.Title)

	for _, artist := range meta.Artists {
		add("ARTIST", artist.Name)
	}

	if meta.Album != nil {
		add("ALBUM", meta.Album.Name)
		add("LABEL", meta.Album.Label)

		for _, artist := range meta.Album.Artists {
			add("ALBUMARTIST", artist.Name)
		}

		if meta.Album.Date != nil && meta.Album.Date.Year != nil {
			add("DATE", strconv.Itoa(int(*meta.Album.Date.Year)))
		}
	}

	if meta.Track.TrackNumber != nil {
		add("TRACKNUMBER", strconv.Itoa(int(*meta.Track.TrackNumber)))
	}

	if meta.Track.DiscNumber != nil {
		add("DISCNUMBER", strconv.Itoa(int(*meta.Track.DiscNumber)))
	}

	add("ISRC", meta.Track.ISRC)
	add("SPOTIFY_URI", meta.Spotify.URI)
	add("SPOTIFY_TRACK_ID", meta.Spotify.TrackID)
	add("SPOTIFY_FILE_ID", meta.Spotify.FileID)

	return tags
}

func isTagged(oggPath string) (bool, error) {
	cmd := exec.Command("vorbiscomment", "-l", oggPath)

	output, err := cmd.Output()
	if err != nil {
		return false, err
	}

	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == taggedMarker {
			return true, nil
		}
	}

	return false, nil
}

func writeTagsAtomic(oggPath string, comments []string, mode os.FileMode) error {
	dir := filepath.Dir(oggPath)

	tmp, err := os.CreateTemp(dir, ".audiotape-tagger-*.ogg")
	if err != nil {
		return err
	}

	tmpPath := tmp.Name()

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}

	defer os.Remove(tmpPath)

	args := []string{
		"-w",
		"-R",
	}

	for _, comment := range comments {
		args = append(args, "-t", comment)
	}

	args = append(args, oggPath, tmpPath)

	cmd := exec.Command("vorbiscomment", args...)

	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf(
			"vorbiscomment: %w: %s",
			err,
			strings.TrimSpace(string(output)),
		)
	}

	if err := os.Chmod(tmpPath, mode.Perm()); err != nil {
		return fmt.Errorf("chmod temporary ogg: %w", err)
	}

	if err := replaceFile(tmpPath, oggPath); err != nil {
		return err
	}

	return nil
}

func replaceFile(source, target string) error {
	backup := target + ".audiotape-backup"

	_ = os.Remove(backup)

	if err := os.Rename(target, backup); err != nil {
		return fmt.Errorf("create backup: %w", err)
	}

	if err := os.Rename(source, target); err != nil {
		_ = os.Rename(backup, target)
		return fmt.Errorf("replace ogg: %w", err)
	}

	if err := os.Remove(backup); err != nil {
		return fmt.Errorf("remove backup: %w", err)
	}

	return nil
}

func artistDisplay(meta metadata) string {
	var artists []string

	for _, artist := range meta.Artists {
		name := strings.TrimSpace(artist.Name)

		if name != "" {
			artists = append(artists, name)
		}
	}

	if len(artists) == 0 {
		return "Unknown Artist"
	}

	return strings.Join(artists, ", ")
}
