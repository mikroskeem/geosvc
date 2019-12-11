package main

import (
	"encoding/json"
	"log"
	"net"
	"net/http"

	lru "github.com/hashicorp/golang-lru"
	geoip2 "github.com/oschwald/geoip2-golang"
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
	db, err := geoip2.Open("./GeoLite2-Country.mmdb")
	if err != nil {
		log.Fatalf("failed to open geoip database: %s", err)
	}
	defer func() { _ = db.Close() }()
	ipCache, err := lru.NewARC(1024)
	if err != nil {
		log.Panic(err)
	}

	http.ListenAndServe("127.0.0.1:5000", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		var country *string = nil
		if cached, ok := ipCache.Get(normalizedIP); ok {
			country = cached.(*string)
		} else {
			record, err := db.Country(ip)
			if err != nil {
				writeResponse(w, http.StatusInternalServerError, StatusError, err)
				return
			}

			country = &record.Country.IsoCode
			ipCache.Add(normalizedIP, country)
		}

		writeResponse(w, http.StatusOK, StatusOK, struct {
			IP      string  `json:"ip"`
			Country *string `json:"country"`
		}{
			IP:      normalizedIP,
			Country: country,
		})
	}))
}
