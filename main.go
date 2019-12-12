package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru"
	geoip2 "github.com/oschwald/geoip2-golang"
)

const (
	StatusOK    = "ok"
	StatusError = "error"

	CountryDBURL    = "https://geolite.maxmind.com/download/geoip/database/GeoLite2-Country.tar.gz"
	CountryDBMD5URL = CountryDBURL + ".md5"
)

var (
	ErrorDatabaseNotOpen = errors.New("GeoIP database not open")
)

type GeoIPDatabase struct {
	db    *geoip2.Reader
	cache *lru.ARCCache
	mtx   sync.RWMutex
}

func NewGeoIPDatabase() *GeoIPDatabase {
	ipCache, err := lru.NewARC(1024)
	if err != nil {
		log.Panic(err)
	}

	return &GeoIPDatabase{
		cache: ipCache,
	}
}

func (g *GeoIPDatabase) CheckAndPullUpdate() error {
	g.mtx.Lock()
	defer g.mtx.Unlock()

	// TODO! actually pull updte
	if g.db == nil {
		db, err := geoip2.Open("./GeoLite2-Country.mmdb")
		if err != nil {
			return err
		}

		g.db = db
	}

	return nil
}

func (g *GeoIPDatabase) GetCountry(IP net.IP) (*geoip2.Country, error) {
	g.mtx.RLock()
	defer g.mtx.RUnlock()

	if g.db == nil {
		return nil, ErrorDatabaseNotOpen
	}

	normalizedIP := IP.String()
	var country *geoip2.Country
	if cached, ok := g.cache.Get(normalizedIP); ok {
		country = cached.(*geoip2.Country)
	} else {
		record, err := g.db.Country(IP)
		if err != nil {
			return nil, err
		}
		country = record

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

func writeResponse(w http.ResponseWriter, httpStatus int, status string, data interface{}) error {
	w.WriteHeader(httpStatus)
	return json.NewEncoder(w).Encode(struct {
		Status string      `json:"status"`
		Data   interface{} `json:"data"`
	}{
		Status: status,
		Data:   data,
	})
}

func main() {
	done := make(chan bool, 1)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	db := NewGeoIPDatabase()
	if err := db.CheckAndPullUpdate(); err != nil {
		log.Fatalf("failed to set up geoip database: %s", err)
	}
	defer func() { _ = db.Close() }()

	// Set up automatic database updater
	updateTicker := time.NewTicker(7 * 24 * time.Hour)
	go func() {
		for {
			select {
			case <-done:
				break
			case <-updateTicker.C:
				log.Print("checking for GeoIP database updates")
				if err := db.CheckAndPullUpdate(); err != nil {
					log.Fatalf("failed pull geoip database update: %s", err)
				}
			}
		}
	}()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			writeResponse(w, http.StatusMethodNotAllowed, StatusError, "method not allowed")
			return
		}

		// Parse the damned address
		var ipRequest struct {
			IP string `json:"ip"`
		}
		body := http.MaxBytesReader(w, r.Body, 2048)
		if err := json.NewDecoder(body).Decode(&ipRequest); err != nil {
			writeResponse(w, http.StatusBadRequest, StatusError, err)
			return
		}

		var ip net.IP
		if ip = net.ParseIP(ipRequest.IP); ip == nil {
			writeResponse(w, http.StatusBadRequest, StatusError, "failed to parse ip")
			return
		}
		normalizedIP := ip.String()

		// Lookup and cache
		country, err := db.GetCountry(ip)
		if err != nil {
			writeResponse(w, http.StatusInternalServerError, StatusError, err)
			return
		}

		writeResponse(w, http.StatusOK, StatusOK, struct {
			IP      string `json:"ip"`
			Country string `json:"country"`
		}{
			IP:      normalizedIP,
			Country: country.Country.IsoCode,
		})
	})

	srv := &http.Server{
		Handler: handler,
		Addr:    "127.0.0.1:5000",
	}

	go func() {
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("failed to serve http: %s", err)
		}
	}()

	// Wait for a signal
	<-sig
	updateTicker.Stop()
	done <- true

	// It's time to go
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("failed to shut down http server: %s", err)
	}
}
