package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	arc "github.com/hashicorp/golang-lru/arc/v2"
	maxminddb "github.com/oschwald/maxminddb-golang/v2"
)

const (
	DatabaseURL    = "https://download.maxmind.com/app/geoip_download?edition_id=@EDITION_ID@&license_key=@LICENSE_KEY@&suffix=tar.gz"
	DatabaseMD5URL = DatabaseURL + ".md5"
)

var (
	ErrorDatabaseNotOpen           = errors.New("GeoIP database not open")
	ErrorDatabaseChecksumMismatch  = errors.New("GeoIP database checksum mismatch")
	ErrorDatabaseNotFoundInArchive = errors.New("GeoIP database not found in downloaded archive")
)

type GeoIPRecord struct {
	Country struct {
		ISOCode *string `maxminddb:"iso_code"`
	} `maxminddb:"country"`

	// City information - can be missing when City database is not used
	City *struct {
		Names struct {
			En string `maxminddb:"en"`
		} `maxminddb:"names"`
	} `maxminddb:"city"`

	// Location information - can be missing when City database is not used
	Location *struct {
		AccuracyRadius uint16  `maxminddb:"accuracy_radius"`
		Latitude       float64 `maxminddb:"latitude"`
		Longitude      float64 `maxminddb:"longitude"`
		Timezone       string  `maxminddb:"time_zone"`
	} `maxminddb:"location"`
}

type GeoIPDatabase struct {
	dir     string
	edition string
	db      *maxminddb.Reader
	cache   *arc.ARCCache[netip.Addr, *GeoIPRecord]
	mtx     sync.RWMutex
}

func NewGeoIPDatabase(dataDirectory string, cacheSize int, edition string) *GeoIPDatabase {
	ipCache, err := arc.NewARC[netip.Addr, *GeoIPRecord](cacheSize)
	if err != nil {
		log.Panic(err)
	}

	return &GeoIPDatabase{
		dir:     dataDirectory,
		edition: edition,
		db:      nil,
		cache:   ipCache,
		mtx:     sync.RWMutex{},
	}
}

func (g *GeoIPDatabase) SetupDatabase(accountId string, licenseKey string) error {
	g.mtx.Lock()
	defer g.mtx.Unlock()

	databasePath := filepath.Join(g.dir, g.edition+".mmdb")
	builtURL := strings.ReplaceAll(strings.ReplaceAll(DatabaseURL, "@LICENSE_KEY@", licenseKey), "@EDITION_ID@", g.edition)
	builtMD5URL := strings.ReplaceAll(strings.ReplaceAll(DatabaseMD5URL, "@LICENSE_KEY@", licenseKey), "@EDITION_ID@", g.edition)

	// Determine if update should be downloaded
	lastDownloadedChecksum := ""
	shouldDownload := false
	lastDownloadedChecksumPath := filepath.Join(g.dir, g.edition+".mmdb.md5")
	if !fileExists(databasePath) || !fileExists(lastDownloadedChecksumPath) {
		// Can't be sure, let's download
		log.Print("either database or its last checksum is not present, will download new database")
		shouldDownload = true
	} else {
		log.Print("checking for database updates...")

		// Read last downloaded checksum
		if d, err := os.ReadFile(lastDownloadedChecksumPath); err != nil {
			return err
		} else {
			lastDownloadedChecksum = string(d)
		}

		// Download remote
		if resp, err := http.Get(builtMD5URL); err != nil {
			return err
		} else if checksum, err := io.ReadAll(resp.Body); err != nil {
			return err
		} else if normalizedChecksum := strings.TrimSpace(string(checksum)); normalizedChecksum != lastDownloadedChecksum {
			// Download the database
			log.Print("update available")
			shouldDownload = true
			lastDownloadedChecksum = normalizedChecksum
		} else {
			// No update found, simply return if database is already set up
			log.Print("no update found")
			if g.db != nil {
				return nil
			}
		}
	}

	// Download
	if shouldDownload {
		log.Print("downloading new database")

		databaseArchivePath := filepath.Join(g.dir, g.edition+".tar.gz")
		newDatabasePath := filepath.Join(g.dir, g.edition+".mmdb.new")
		newChecksumPath := filepath.Join(g.dir, g.edition+"-last-downloaded.md5.new")

		// Download the database archive
		downloadedDatabaseArchiveChecksum := ""
		if r, err := http.Get(builtURL); err != nil {
			return err
		} else {
			if f, err := os.Create(databaseArchivePath); err != nil {
				return err
			} else {
				defer func() { _ = f.Close() }()

				h := md5.New()
				r := io.TeeReader(r.Body, h)

				if _, err := io.Copy(f, r); err != nil {
					return nil
				}

				downloadedDatabaseArchiveChecksum = fmt.Sprintf("%x", h.Sum(nil))
			}
		}

		// Also download checksum if it's not downloaded yet
		if len(lastDownloadedChecksum) == 0 {
			if resp, err := http.Get(builtMD5URL); err != nil {
				return err
			} else if checksum, err := io.ReadAll(resp.Body); err != nil {
				return err
			} else {
				lastDownloadedChecksum = strings.ToLower(strings.TrimSpace(string(checksum)))
			}
		}

		// Compare checksums
		if downloadedDatabaseArchiveChecksum != lastDownloadedChecksum {
			log.Printf("%s != %s", downloadedDatabaseArchiveChecksum, lastDownloadedChecksum)
			//_ = os.Remove(newDatabasePath)
			//_ = os.Remove(newChecksumPath)
			return ErrorDatabaseChecksumMismatch
		}

		// Unpack database archive and find the mmdb file
		if archive, err := os.Open(databaseArchivePath); err != nil {
			return err
		} else if gr, err := gzip.NewReader(archive); err != nil {
			return err
		} else {
			defer func() { _ = archive.Close() }()
			defer func() { _ = gr.Close() }()
			tr := tar.NewReader(gr)

			databaseFound := false
			for {
				h, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}

				baseName := path.Base(h.Name)
				if baseName != g.edition+".mmdb" {
					continue
				}
				// Database file exists, also copy it over
				databaseFound = true

				// Open database file
				if f, err := os.Create(newDatabasePath); err != nil {
					return err
				} else {
					defer func() { _ = f.Close() }()
					if _, err := io.Copy(f, tr); err != nil {
						return err
					}
				}

				break
			}

			if !databaseFound {
				return ErrorDatabaseNotFoundInArchive
			}

			log.Print("database downloaded")

			// Delete database archive
			if err := os.Remove(databaseArchivePath); err != nil {
				log.Printf("failed to delete database archive: %s", err)
			}
		}

		// Save checksum
		if err := os.WriteFile(newChecksumPath, []byte(lastDownloadedChecksum), 0644); err != nil {
			log.Printf("failed to save last downloaded checksum: %s", err)
		}

		// Atomically replace database and its checksum files
		if err := os.Rename(newDatabasePath, databasePath); err != nil {
			return err
		}
		if err := os.Rename(newChecksumPath, lastDownloadedChecksumPath); err != nil {
			return err
		}
	}

	if g.db != nil {
		if err := g.db.Close(); err != nil {
			fmt.Printf("failed to close previous database: %s", err)
		}
	}

	// Open database
	db, err := maxminddb.Open(databasePath)
	if err != nil {
		return err
	}

	g.db = db
	g.cache.Purge()
	log.Print("database set up")

	return nil
}

func (g *GeoIPDatabase) GetRecord(ip netip.Addr) (*GeoIPRecord, error) {
	g.mtx.RLock()
	defer g.mtx.RUnlock()

	if g.db == nil {
		return nil, ErrorDatabaseNotOpen
	}

	if cached, ok := g.cache.Get(ip); ok {
		return cached, nil
	}

	var record GeoIPRecord
	err := g.db.Lookup(ip).Decode(&record)
	if err != nil {
		return nil, err
	}

	g.cache.Add(ip, &record)
	return &record, nil
}

func (g *GeoIPDatabase) Close() error {
	g.mtx.Lock()
	defer g.mtx.Unlock()
	if g.db != nil {
		if err := g.db.Close(); err != nil {
			return err
		}
		g.db = nil
	}
	return nil
}

func fileExists(path string) bool {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false
	} else if err == nil {
		return true
	} else {
		// Uhh
		panic(err)
	}
}
