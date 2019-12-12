package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"
)

const (
	StatusOK    = "ok"
	StatusError = "error"
)

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

		// Lookup
		country, err := db.GetCountryISOCode(ip)
		if err != nil {
			writeResponse(w, http.StatusInternalServerError, StatusError, err)
			return
		}

		writeResponse(w, http.StatusOK, StatusOK, struct {
			IP      string  `json:"ip"`
			Country *string `json:"country"`
		}{
			IP:      normalizedIP,
			Country: country,
		})
	})

	srv := &http.Server{
		Handler:      handler,
		Addr:         "127.0.0.1:5000",
		WriteTimeout: 15 * time.Second,
		ReadTimeout:  15 * time.Second,
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
