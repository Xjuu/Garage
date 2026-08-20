package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif" // registers GIF decoding with image.Decode
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// Part photos come from Wikimedia Commons, not a general web image search:
// it is free, needs no API key, and — unlike scraping search results — every
// file on it actually carries a licence that permits reuse. The trade is
// coverage: Commons is an encyclopaedia's media library, not a parts
// catalogue, so a search for a specific manufacturer SKU will often find
// nothing at all. That is treated as the normal case, not an error — a
// missing photo just means nobody has uploaded one for this part yet, by
// either path.
const wikimediaAPI = "https://commons.wikimedia.org/w/api.php"

// A descriptive, non-personal user agent — Commons' API asks every caller to
// identify itself; this names the tool, nothing about who runs any
// particular install of it.
const partsPhotoUserAgent = "GoldstarPartsCounter/1.0"

// photoDir is where fetched and uploaded part photos live, one PNG per part
// number — same "small local folder of images" pattern as the vehicle icons.
func (s *Server) photoDir() string { return filepath.Join(s.cfg.DataDir, "parts-photos") }

// photoPath hashes the part number into the filename: part numbers can
// contain slashes and other characters that are not safe in a path
// component, and hashing sidesteps that entirely rather than trying to
// sanitise every possible input.
func (s *Server) photoPath(part string) string {
	sum := sha256.Sum256([]byte(part))
	return filepath.Join(s.photoDir(), hex.EncodeToString(sum[:])+".png")
}

// negativeMarker records "already tried Wikimedia for this part, found
// nothing" — so a part with no photo does not re-query Commons on every
// single request for it, which would be both slow and impolite to a free
// public API.
func (s *Server) negativeMarker(part string) string { return s.photoPath(part) + ".none" }

// servePartPhoto answers a photo request from either side: the worker
// counter (IP-gated) and the admin Parts page (dashboard-auth-gated) share
// this one handler, registered under each mux with its own surrounding
// middleware — the logic here has no idea which caller reached it, and does
// not need to.
func (s *Server) servePartPhoto(w http.ResponseWriter, r *http.Request) {
	part := r.PathValue("part")
	if part == "" {
		http.NotFound(w, r)
		return
	}
	path := s.photoPath(part)

	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFile(w, r, path)
		return
	}
	if _, err := os.Stat(s.negativeMarker(part)); err == nil {
		http.NotFound(w, r)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	data, err := fetchWikimediaPhoto(ctx, part)
	if err != nil {
		os.MkdirAll(s.photoDir(), 0o700)
		os.WriteFile(s.negativeMarker(part), []byte(err.Error()), 0o600)
		http.NotFound(w, r)
		return
	}
	os.MkdirAll(s.photoDir(), 0o700)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		http.Error(w, "could not save photo", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Write(data)
}

// uploadPartPhoto lets the admin page attach or replace a photo by hand —
// the reliable path, since an automatic search over a specific manufacturer
// part number will frequently find nothing worth showing.
func (s *Server) uploadPartPhoto(r *http.Request) (any, error) {
	part := r.PathValue("part")
	if part == "" {
		return nil, fail(http.StatusBadRequest, "part number is required")
	}
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		return nil, fail(http.StatusBadRequest, "bad upload: %v", err)
	}
	file, _, err := r.FormFile("photo")
	if err != nil {
		return nil, fail(http.StatusBadRequest, "no photo in the upload")
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return nil, fail(http.StatusBadRequest, "that file doesn't look like an image: %v", err)
	}

	if err := os.MkdirAll(s.photoDir(), 0o700); err != nil {
		return nil, err
	}
	out, err := os.Create(s.photoPath(part))
	if err != nil {
		return nil, err
	}
	defer out.Close()
	if err := png.Encode(out, img); err != nil {
		return nil, err
	}
	os.Remove(s.negativeMarker(part)) // a manual upload always wins over a cached "nothing found"
	return okResponse(), nil
}

// fetchWikimediaPhoto searches Commons for the part number, resolves the
// first hit to an actual image URL, downloads it, and re-encodes it as PNG —
// never hotlinked, never served in whatever format Commons happened to
// store it in.
func fetchWikimediaPhoto(ctx context.Context, part string) ([]byte, error) {
	title, err := wikimediaSearch(ctx, part)
	if err != nil {
		return nil, err
	}
	imgURL, err := wikimediaImageURL(ctx, title)
	if err != nil {
		return nil, err
	}
	return downloadAsPNG(ctx, imgURL)
}

func wikimediaSearch(ctx context.Context, q string) (title string, err error) {
	var body struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	u := fmt.Sprintf("%s?action=query&list=search&srsearch=%s&srnamespace=6&srlimit=1&format=json",
		wikimediaAPI, url.QueryEscape(q))
	if err := wikimediaGetJSON(ctx, u, &body); err != nil {
		return "", err
	}
	if len(body.Query.Search) == 0 {
		return "", fmt.Errorf("no Commons file matches %q", q)
	}
	return body.Query.Search[0].Title, nil
}

func wikimediaImageURL(ctx context.Context, title string) (string, error) {
	var body struct {
		Query struct {
			Pages map[string]struct {
				ImageInfo []struct {
					URL string `json:"url"`
				} `json:"imageinfo"`
			} `json:"pages"`
		} `json:"query"`
	}
	u := fmt.Sprintf("%s?action=query&titles=%s&prop=imageinfo&iiprop=url&format=json",
		wikimediaAPI, url.QueryEscape(title))
	if err := wikimediaGetJSON(ctx, u, &body); err != nil {
		return "", err
	}
	for _, page := range body.Query.Pages {
		if len(page.ImageInfo) > 0 && page.ImageInfo[0].URL != "" {
			return page.ImageInfo[0].URL, nil
		}
	}
	return "", fmt.Errorf("Commons returned no image URL for %q", title)
}

func wikimediaGetJSON(ctx context.Context, reqURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", partsPhotoUserAgent)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("wikimedia: unexpected status %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(out)
}

func downloadAsPNG(ctx context.Context, imgURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imgURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", partsPhotoUserAgent)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wikimedia: image fetch status %d", res.StatusCode)
	}

	img, _, err := image.Decode(io.LimitReader(res.Body, 20<<20))
	if err != nil {
		// SVG and WebP sources land here — the standard decoders above
		// don't cover them, and that is an acceptable gap rather than a
		// reason to pull in more dependencies for what is already a
		// best-effort feature.
		return nil, fmt.Errorf("could not decode image from Commons: %w", err)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
