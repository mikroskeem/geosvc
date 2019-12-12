package main

import (
	"errors"
	"log"
	"net"
	"sync"

	lru "github.com/hashicorp/golang-lru"
	maxminddb "github.com/oschwald/maxminddb-golang"
)

const (
	CountryDBURL    = "https://geolite.maxmind.com/download/geoip/database/GeoLite2-Country.tar.gz"
	CountryDBMD5URL = CountryDBURL + ".md5"
)

var (
	ErrorDatabaseNotOpen = errors.New("GeoIP database not open")
)

type GeoIPDatabase struct {
	db    *maxminddb.Reader
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
		db, err := maxminddb.Open("./GeoLite2-Country.mmdb")
		if err != nil {
			return err
		}

		g.db = db
	}

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
