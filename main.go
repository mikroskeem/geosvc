package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"time"
)

const (
	StatusOK    = "ok"
	StatusError = "error"
)

type resolvedIPLocation struct {
	City      *string  `json:"city"`
	Accuracy  *uint16  `json:"accuracy"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
	Timezone  *string  `json:"timezone"`
}

type resolvedIP struct {
	IP       *string             `json:"ip"`
	Country  *string             `json:"country"`
	Location *resolvedIPLocation `json:"location,omitempty"`
}

func populateLocation(record *GeoIPRecord) *resolvedIPLocation {
	if record.Location == nil || record.City == nil {
		return nil
	}

	return &resolvedIPLocation{
		City:      &record.City.Names.En,
		Accuracy:  &record.Location.AccuracyRadius,
		Latitude:  &record.Location.Latitude,
		Longitude: &record.Location.Longitude,
		Timezone:  &record.Location.Timezone,
	}
}

func newResolvedIP(ip *string, record *GeoIPRecord) *resolvedIP {
	return &resolvedIP{
		IP:       ip,
		Country:  record.Country.ISOCode,
		Location: populateLocation(record),
	}
}

func writeResponse(w http.ResponseWriter, httpStatus int, status string, data any) {
	w.WriteHeader(httpStatus)
	_ = json.NewEncoder(w).Encode(struct {
		Status string `json:"status"`
		Data   any    `json:"data"`
	}{
		Status: status,
		Data:   data,
	})
}

func main() {
	done := make(chan bool, 1)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	// Grab configuration from the environment
	listenAddress := os.Getenv("GEOSVC_LISTEN_ADDR")
	databaseDir := os.Getenv("GEOSVC_DATA_DIR")
	accountId := os.Getenv("GEOSVC_MAXMIND_ACCOUNT_ID")
	licenseKey := os.Getenv("GEOSVC_MAXMIND_LICENSE_KEY")
	cacheSizeStr := os.Getenv("GEOSVC_CACHE_SIZE")
	cacheSize := 1024
	maxBulkCountryRequestSizeStr := os.Getenv("GEOSVC_MAX_BULK_COUNTRY_REQUEST_SIZE")
	maxBulkCountryRequestSize := int64(2) << 14
	dbEdition := "GeoLite2-Country"

	if len(listenAddress) == 0 {
		listenAddress = "0.0.0.0:5000"
	}
	if len(databaseDir) == 0 {
		databaseDir = "./data"
	}
	if len(accountId) == 0 {
		log.Fatalf("GEOSVC_MAXMIND_ACCOUNT_ID is not set for database downloading and update checks")
	}
	if len(licenseKey) == 0 {
		log.Fatalf("GEOSVC_MAXMIND_LICENSE_KEY is not set for database downloading and update checks")
	}
	if len(cacheSizeStr) > 0 {
		if v, err := strconv.ParseInt(cacheSizeStr, 10, 32); err != nil {
			log.Fatalf("Failed to parse GEOSVC_CACHE_SIZE: %s", err)
		} else {
			cacheSize = int(v)
		}
	}
	if len(maxBulkCountryRequestSizeStr) > 0 {
		if v, err := strconv.ParseInt(maxBulkCountryRequestSizeStr, 10, 64); err != nil {
			log.Fatalf("Failed to parse GEOSVC_MAX_BULK_COUNTRY_REQUEST_SIZE: %s", err)
		} else {
			maxBulkCountryRequestSize = v
		}
	}
	if v := os.Getenv("GEOSVC_DB_EDITION"); len(v) > 0 {
		dbEdition = v
	}

	// Create database directory
	if err := os.MkdirAll(databaseDir, 0755); err != nil {
		log.Panicf("failed to create %s: %s", databaseDir, err)
	}

	db := NewGeoIPDatabase(databaseDir, cacheSize, dbEdition)
	if err := db.SetupDatabase(accountId, licenseKey); err != nil {
		log.Fatalf("failed to set up geoip database: %s", err)
	}
	defer func() { _ = db.Close() }()

	// Set up automatic database updater
	updateTicker := time.NewTicker(2 * 24 * time.Hour)
	go func() {
		for {
			select {
			case <-done:
				break
			case <-updateTicker.C:
				log.Print("checking for GeoIP database updates")
				if err := db.SetupDatabase(accountId, licenseKey); err != nil {
					log.Printf("failed pull geoip database update: %s", err)
				}
			}
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/country", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			writeResponse(w, http.StatusMethodNotAllowed, StatusError, "method not allowed")
			return
		}

		// Parse the damned address
		var ipRequest struct {
			IP string `json:"ip"`
		}
		body := http.MaxBytesReader(w, r.Body, 2<<6)
		if err := json.NewDecoder(body).Decode(&ipRequest); err != nil {
			writeResponse(w, http.StatusBadRequest, StatusError, err)
			return
		}

		ip, err := netip.ParseAddr(ipRequest.IP)
		if err != nil {
			writeResponse(w, http.StatusBadRequest, StatusError, "failed to parse ip")
			return
		}

		// Lookup
		record, err := db.GetRecord(ip)
		if err != nil {
			writeResponse(w, http.StatusInternalServerError, StatusError, err)
			return
		}

		writeResponse(w, http.StatusOK, StatusOK, newResolvedIP(&ipRequest.IP, record))
	})
	mux.HandleFunc("/api/v1/bulkcountry", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			writeResponse(w, http.StatusMethodNotAllowed, StatusError, "method not allowed")
			return
		}

		var bulkIPRequest struct {
			IPs []string `json:"ips"`
		}

		body := http.MaxBytesReader(w, r.Body, int64(maxBulkCountryRequestSize))
		if err := json.NewDecoder(body).Decode(&bulkIPRequest); err != nil {
			writeResponse(w, http.StatusBadRequest, StatusError, err)
			return
		}

		resolvedIPs := make([]resolvedIP, 0, len(bulkIPRequest.IPs))

		for idx, rawIP := range bulkIPRequest.IPs {
			ip, err := netip.ParseAddr(rawIP)
			if err != nil {
				writeResponse(w, http.StatusBadRequest, StatusError, fmt.Sprintf("failed to parse ip at idx: %d", idx))
				return
			}

			record, err := db.GetRecord(ip)
			if err != nil {
				writeResponse(w, http.StatusBadRequest, StatusError, fmt.Sprintf("failed resolve country for ip '%s': %s", ip.String(), err))
				return
			}

			resolvedIPs = append(resolvedIPs, *newResolvedIP(&rawIP, record))
		}

		writeResponse(w, http.StatusOK, StatusOK, resolvedIPs)
	})

	srv := &http.Server{
		Handler:      mux,
		Addr:         listenAddress,
		WriteTimeout: 15 * time.Second,
		ReadTimeout:  15 * time.Second,
	}

	log.Printf("serving http on http://%s", listenAddress)
	go func() {
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("failed to serve http: %s", err)
			done <- true
		}
	}()

	// Wait for a signal or exit flag
	select {
	case <-sig:
		log.Print("caught signal, exiting")
	case <-done:
		// no-op
	}

	updateTicker.Stop()

	// It's time to go
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("failed to shut down http server: %s", err)
	}

	if err := srv.Close(); err != nil {
		log.Printf("failed to close http server: %s", err)
	}
}
