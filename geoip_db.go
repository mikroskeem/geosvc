package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	lru "github.com/hashicorp/golang-lru"
	maxminddb "github.com/oschwald/maxminddb-golang"
)

const (
	CountryDBURL    = "https://download.maxmind.com/app/geoip_download?edition_id=GeoLite2-Country&license_key=@LICENSE_KEY@&suffix=tar.gz"
	CountryDBMD5URL = CountryDBURL + ".md5"

	CountryDBName    = "GeoLite2-Country.mmdb"
	CountryDBMD5Name = CountryDBName + ".md5"
)

var (
	ErrorDatabaseNotOpen           = errors.New("GeoIP database not open")
	ErrorDatabaseChecksumMismatch  = errors.New("GeoIP database checksum mismatch")
	ErrorDatabaseNotFoundInArchive = errors.New("GeoIP database not found in downloaded archive")
)

type GeoIPDatabase struct {
	dir   string
	db    *maxminddb.Reader
	cache *lru.ARCCache
	mtx   sync.RWMutex
}

func NewGeoIPDatabase(dataDirectory string, cacheSize int) *GeoIPDatabase {
	ipCache, err := lru.NewARC(cacheSize)
	if err != nil {
		log.Panic(err)
	}

	return &GeoIPDatabase{
		dir:   dataDirectory,
		cache: ipCache,
	}
}

func (g *GeoIPDatabase) SetupDatabase(licenseKey string) error {
	g.mtx.Lock()
	defer g.mtx.Unlock()

	databasePath := filepath.Join(g.dir, CountryDBName)
	builtURL := strings.ReplaceAll(CountryDBURL, "@LICENSE_KEY@", licenseKey)
	builtMD5URL := strings.ReplaceAll(CountryDBMD5URL, "@LICENSE_KEY@", licenseKey)

	// Determine if update should be downloaded
	lastDownloadedChecksum := ""
	shouldDownload := false
	lastDownloadedChecksumPath := filepath.Join(g.dir, CountryDBMD5Name)
	if !fileExists(databasePath) || !fileExists(lastDownloadedChecksumPath) {
		// Can't be sure, let's download
		log.Print("either database or its last checksum is not present, will download new database")
		shouldDownload = true
	} else {
		log.Print("checking for database updates...")

		// Read last downloaded checksum
		if d, err := ioutil.ReadFile(lastDownloadedChecksumPath); err != nil {
			return err
		} else {
			lastDownloadedChecksum = string(d)
		}

		// Download remote
		if resp, err := http.Get(builtMD5URL); err != nil {
			return err
		} else if checksum, err := ioutil.ReadAll(resp.Body); err != nil {
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

		databaseArchivePath := filepath.Join(g.dir, "GeoLite2-Country.tar.gz")
		newDatabasePath := filepath.Join(g.dir, "GeoLite2-Country.mmdb.new")
		newChecksumPath := filepath.Join(g.dir, "last-downloaded.md5.new")

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
			} else if checksum, err := ioutil.ReadAll(resp.Body); err != nil {
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
				if baseName != "GeoLite2-Country.mmdb" {
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
		if err := ioutil.WriteFile(newChecksumPath, []byte(lastDownloadedChecksum), 0644); err != nil {
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

func (g *GeoIPDatabase) GetCountryISOCode(IP net.IP) (*string, error) {
	g.mtx.RLock()
	defer g.mtx.RUnlock()

	if g.db == nil {
		return nil, ErrorDatabaseNotOpen
	}

	normalizedIP := IP.String()
	var country *string
	if cached, ok := g.cache.Get(normalizedIP); ok {
		country = cached.(*string)
	} else {
		var record struct {
			Country struct {
				ISOCode *string `maxminddb:"iso_code"`
			} `maxminddb:"country"`
		}

		err := g.db.Lookup(IP, &record)
		if err != nil {
			return nil, err
		}
		country = record.Country.ISOCode

		g.cache.Add(normalizedIP, country)
	}

	return country, nil
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
