package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager"
	"code.cloudfoundry.org/lager/lagerctx"
	"golang.org/x/oauth2"
)

var numConsumers = flag.Int("n", 1, "number of API consumers")
var duration = flag.Duration("duration", time.Hour, "how long to run the experiment")
var delay = flag.Duration("delay", 5*time.Second, "delay between API requests")
var endpoint = flag.String("endpoint", "/api/v1/jobs", "API endpoint to hit")
var rawHeaders = flag.String("headers", "", "headers to append to each request (K1:V1,K2:V2,...)")
var decompress = flag.Bool("decompress", false, "if set, the bytes received reported will be the size of the decompressed API response, rather than the amount of (gzipped) data sent over the wire")
var logLevel = flag.String("log-level", "info", "debug, info, error, or fatal")

type ConsumerStats struct {
	TotalRequests      uint64
	SuccessfulRequests uint64
	FailedRequests     uint64
	ErroredRequests    uint64

	TotalTimeToFirstByteNano time.Duration

	TotalBytesReceived uint64
}

type SummaryStats struct {
	TotalRequests      uint64
	SuccessfulRequests uint64
	FailedRequests     uint64
	ErroredRequests    uint64

	AvgTimeToFirstByteNano time.Duration

	TotalBytesReceived uint64
}

func (s SummaryStats) LagerData() lager.Data {
	raw, _ := json.Marshal(s)
	var data lager.Data
	json.Unmarshal(raw, &data)
	return data
}

func main() {
	flag.Parse()

	logger := lager.NewLogger("loadtest")
	logLevel, err := lager.LogLevelFromString(*logLevel)
	logger.RegisterSink(lager.NewWriterSink(os.Stderr, logLevel))

	atcURL, ok := os.LookupEnv("LOADTEST_ATC_URL")
	if !ok {
		panic("must specify LOADTEST_ATC_URL")
	}

	atcURL = strings.TrimRight(atcURL, "/")

	username, ok := os.LookupEnv("LOADTEST_ADMIN_USERNAME")
	if !ok {
		panic("must specify LOADTEST_ADMIN_USERNAME")
	}

	password, ok := os.LookupEnv("LOADTEST_ADMIN_PASSWORD")
	if !ok {
		panic("must specify LOADTEST_ADMIN_PASSWORD")
	}

	headers := http.Header{}
	if !*decompress {
		headers.Set("Accept-Encoding", "gzip")
	}
	if *rawHeaders != "" {
		for _, seg := range strings.Split(*rawHeaders, ",") {
			parts := strings.SplitN(seg, ":", 2)
			if len(parts) != 2 {
				panic("invalid value for -headers (expected K1:V1,K2:V2,...)")
			}
			headers.Set(strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	oauthClient, err := authenticatedClient(lagerctx.NewContext(ctx, logger.Session("authenticate")), atcURL, username, password)
	if err != nil {
		panic(fmt.Sprintf("failed to fetch access token: %v", err))
	}

	stats := make([]ConsumerStats, *numConsumers)

	start := time.Now()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	go func() {
		<-sig
		logger.Info("manually-terminated", lager.Data{"duration": time.Since(start)})
		emitStats(logger, os.Stdout, stats)
		os.Exit(1)
	}()

	logger.Info("init", lager.Data{
		"num_consumers": *numConsumers,
		"atc_url":       atcURL,
		"endpoint":      endpoint,
		"headers":       headers,
	})
	var wg sync.WaitGroup
	for i := 0; i < *numConsumers; i++ {
		wg.Add(1)
		session := logger.Session("consumer", lager.Data{"number": i})
		go consume(lagerctx.NewContext(ctx, session), oauthClient, atcURL, *endpoint, headers, *delay, &stats[i], &wg)
	}
	wg.Wait()

	emitStats(logger, os.Stdout, stats)
}

func consume(ctx context.Context, client *http.Client, atcURL, endpoint string, headers http.Header, delay time.Duration, stats *ConsumerStats, wg *sync.WaitGroup) {
	defer wg.Done()

	logger := lagerctx.FromContext(ctx)

	ticker := time.NewTicker(delay)
	sem := make(chan struct{}, 1)
	for {
		select {
		case <-ticker.C:
			select {
			case sem <- struct{}{}:
				req, _ := http.NewRequestWithContext(ctx, "GET", atcURL+endpoint, nil)
				req.Header = headers
				go func(logger lager.Logger) {
					defer func() { <-sem }()

					stats.TotalRequests++

					start := time.Now()
					res, err := client.Do(req)
					if err != nil {
						logger.Error("do", err)
						stats.ErroredRequests++
						return
					}
					defer res.Body.Close()

					if res.StatusCode >= 200 && res.StatusCode < 400 {
						stats.SuccessfulRequests++
					} else {
						stats.FailedRequests++
						logger.Info("failed", lager.Data{"code": res.StatusCode})
					}

					firstRead := true
					var buf [1024]byte
					for {
						select {
						case <-ctx.Done():
							return
						default:
						}
						n, err := res.Body.Read(buf[:])
						if firstRead {
							stats.TotalTimeToFirstByteNano += time.Since(start)
							firstRead = false
						}
						stats.TotalBytesReceived += uint64(n)
						if err == io.EOF {
							return
						} else if err != nil {
							stats.ErroredRequests++
							logger.Error("read-body", err)
							return
						}
					}
				}(logger.Session("request", lager.Data{"number": stats.TotalRequests}))
			default:
				// still awaiting a response from a previous request...loop around
			}

		case <-ctx.Done():
			return
		}
	}
}

func authenticatedClient(ctx context.Context, atcURL, username, password string) (*http.Client, error) {
	logger := lagerctx.FromContext(ctx)
	logger.Debug("requesting-token")

	oauth2Config := oauth2.Config{
		ClientID:     "fly",
		ClientSecret: "Zmx5",
		Endpoint:     oauth2.Endpoint{TokenURL: atcURL + "/sky/issuer/token"},
		Scopes:       []string{"openid", "profile", "email", "federated:id", "groups"},
	}

	token, err := oauth2Config.PasswordCredentialsToken(ctx, username, password)
	if err != nil {
		return nil, err
	}
	logger.Debug("success")

	return oauth2Config.Client(ctx, token), nil
}

func emitStats(logger lager.Logger, out io.Writer, stats []ConsumerStats) {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	enc.Encode(stats)

	summary := SummaryStats{}
	var rollingAvgTimeToFirstByteNano float64
	for _, stat := range stats {
		prevTotalRequests := summary.TotalRequests

		summary.TotalRequests += stat.TotalRequests
		summary.SuccessfulRequests += stat.SuccessfulRequests
		summary.FailedRequests += stat.FailedRequests
		summary.ErroredRequests += stat.ErroredRequests
		summary.TotalBytesReceived += stat.TotalBytesReceived

		rollingAvgTimeToFirstByteNano = rollingAverage(rollingAvgTimeToFirstByteNano, prevTotalRequests, float64(stat.TotalTimeToFirstByteNano), stat.TotalRequests)
	}
	summary.AvgTimeToFirstByteNano = time.Duration(rollingAvgTimeToFirstByteNano)

	logger.Info("summary", summary.LagerData())
}

func rollingAverage(avgN float64, n uint64, totalM float64, m uint64) float64 {
	if n+m == 0 {
		return 0
	}
	return avgN*float64(n)/float64(n+m) + totalM/float64(n+m)
}
