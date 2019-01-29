package ratelimit

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mholt/caddy/caddyhttp/httpserver"
)

// RateLimit is an http.Handler that can limit request rate to specific paths or files
type RateLimit struct {
	Next  httpserver.Handler
	Rules []Rule
}

// Rule is a configuration for ratelimit
type Rule struct {
	Methods   string
	Rate      int64
	Burst     int
	Unit      string
	Whitelist []string
	Status    string
	Resources []string
}

const (
	ignoreSymbol = "^"
)

var (
	caddyLimiter *CaddyLimiter
)

func init() {

	caddyLimiter = NewCaddyLimiter()
}

// ServeHTTP is the method handling every request
func (rl RateLimit) ServeHTTP(w http.ResponseWriter, r *http.Request) (nextResponseStatus int, err error) {

	retryAfter := time.Duration(0)
	// get request ip address
	ipAddress, err := GetRemoteIP(r)
	if err != nil {
		return http.StatusInternalServerError, err
	}

	for _, rule := range rl.Rules {
		for _, res := range rule.Resources {

			// handle `ignore`
			if strings.HasPrefix(res, ignoreSymbol) {
				res = strings.TrimPrefix(res, ignoreSymbol)
				if httpserver.Path(r.URL.Path).Matches(res) {
					return rl.Next.ServeHTTP(w, r)
				}
			}

			// handle path mismatch
			if !httpserver.Path(r.URL.Path).Matches(res) {
				continue
			}

			// handle whitelist ip & method mismatch
			if IsWhitelistIPAddress(ipAddress, whitelistIPNets) || !MatchMethod(rule.Methods, r.Method) {
				continue
			}

			/*
				check if this ip has already exceeded quota
				if so, reject all the subsequent requests

				note: this won't block resources outside of the plugin's config
			*/
			sliceKeysOnlyWithIP := buildKeysOnlyWithIP(ipAddress)
			for _, keys := range sliceKeysOnlyWithIP {
				ret := caddyLimiter.Allow(keys, rule)
				if !ret {
					// fmt.Printf("limiting in 1 \n")
					// fmt.Printf("applied rule: %v\n", rule)
					retryAfter = caddyLimiter.RetryAfter(keys)
					w.Header().Add("X-RateLimit-RetryAfter", retryAfter.String())
					return http.StatusTooManyRequests, err
				}
			}

			// check limit
			if len(rule.Status) == 0 || rule.Status == "*" {
				sliceKeys := buildKeys(ipAddress, rule.Methods, rule.Status, res)
				for _, keys := range sliceKeys {
					ret := caddyLimiter.Allow(keys, rule)
					if !ret {
						// fmt.Printf("limiting in 2 \n")
						// fmt.Printf("applied rule: %v\n", rule)
						retryAfter = caddyLimiter.RetryAfter(keys)
						w.Header().Add("X-RateLimit-RetryAfter", retryAfter.String())
						return http.StatusTooManyRequests, err
					}
				}
			}
		}
	}

	/*
		special case for limiting by response status code
	*/
	nextResponseStatus, err = rl.Next.ServeHTTP(w, r)

	for _, rule := range rl.Rules {

		// handle response status code mismatch
		if !MatchStatus(rule.Status, strconv.Itoa(nextResponseStatus)) {
			continue
		}
		for _, res := range rule.Resources {

			// handle `ignore`
			if strings.HasPrefix(res, ignoreSymbol) {
				res = strings.TrimPrefix(res, ignoreSymbol)
				if httpserver.Path(r.URL.Path).Matches(res) {
					return nextResponseStatus, err
				}
			}

			// handle path mismatch
			if !httpserver.Path(r.URL.Path).Matches(res) {
				continue
			}

			// handle whitelist ip & method mismatch
			if IsWhitelistIPAddress(ipAddress, whitelistIPNets) || !MatchMethod(rule.Methods, r.Method) {
				continue
			}

			sliceKeys := buildKeysOnlyWithIP(ipAddress)
			for _, keys := range sliceKeys {
				// consume one token if status code matches
				// fmt.Printf("reserve in rule: %v", rule)
				caddyLimiter.Reserve(keys)
			}
		}
	}

	return nextResponseStatus, err
}
