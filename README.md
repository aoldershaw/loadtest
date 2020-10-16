# loadtest

Used to load test Concourse by repeatedly hitting an endpoint every `-delay`
seconds (5 by default) using `-n` concurrent connections. This is to simulate
the behaviour of a whole bunch of users keeping their dashboard open.

(see concourse/concourse#6084)

## usage

```
$ docker run --rm \
  -e LOADTEST_ATC_URL="..." \
  -e LOADTEST_ADMIN_USERNAME="..." \
  -e LOADTEST_ADMIN_PASSWORD="..." \
  aoldershaw/loadtest \
  -n 100 \
  -duration 1h
```

help text:

```
  -decompress
    	if set, the bytes received reported will be the size of the decompressed API response, rather than the amount of (gzipped) data sent over the wire
  -delay duration
    	delay between API requests (default 5s)
  -duration duration
    	how long to run the experiment (default 1h0m0s)
  -endpoint string
    	API endpoint to hit (default "/api/v1/jobs")
  -headers string
    	headers to append to each request (K1:V1,K2:V2,...)
  -log-level string
    	debug, info, error, or fatal (default "info")
  -n int
    	number of API consumers (default 1)
```
