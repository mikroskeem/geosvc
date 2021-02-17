# geosvc

Simple MaxMind GeoIP country database microservice

Note that only GeoLite2 country database is supported at the moment.

## Usage

### Setting up

Run `go build .` or `./docker/build_docker.sh` to get either binary or Docker image.

### Getting the license key for database downloading

Get GeoIP license key from [MaxMind site](https://www.maxmind.com/en/accounts/current/license-key), at the time of writing (2021-02-17)
you can get it for free.

#### Environment variables

- `GEOSVC_MAXMIND_LICENSE_KEY` - you need to set this for geosvc to operate. It's used for fetching and updating the database
- `GEOSVC_LISTEN_ADDR` - takes `host:port` pair. Default value is `0.0.0.0:5000`
- `GEOSVC_DATA_DIR` - takes a path where geosvc can store its data. Default value is `./data`
- `GEOSVC_CACHE_SIZE` - ARC cache size (n >= 1). Default value is `1024`

### Automatic database updates

Currently database update will be performed on startup and every 2 days. There is no way to turn automatic update off at the moment.

### API endpoints

Currently this microservice exposes only one endpoint.

It does not check Content-Type nor Accepts header on any endpoints, it will try to parse and send json blindly.

#### /api/v1/country

Method: `POST`

* Both IPv6 and IPv4 are supported - IPv6 should be supplied without square brackets.
* POST body cannot be larger than 2048 bytes.
* JSON response will always contain object with keys `"status"` and `"data"`. Status can be either `"ok"` or `"error"`
* In case of error, the response code will never be `200` and `"data"` will be string describing the issue (best effort).
* In case of success, response code will be 200 and `"data"` will be object containing (normalized) IP address and country ISO code (if found - otherwise it'll be null).


Example of the request and response:

```
curl -v -H 'Content-Type: application/json' -d '{"ip":"195.50.209.246"}' http://127.0.0.1:5000/api/v1/country
*   Trying 127.0.0.1:5000...
* Connected to 127.0.0.1 (127.0.0.1) port 5000 (#0)
> POST /api/v1/country HTTP/1.1
> Host: 127.0.0.1:5000
> User-Agent: curl/7.75.0
> Accept: */*
> Content-Type: application/json
> Content-Length: 23
>
* Mark bundle as not supporting multiuse
< HTTP/1.1 200 OK
< Content-Type: application/json
< Date: Tue, 16 Feb 2021 10:43:50 GMT
< Content-Length: 62
<
{"status":"ok","data":{"ip":"195.50.209.246","country":"EE"}}
* Connection #0 to host 127.0.0.1 left intact
```

## License

GPLv3
