package model

import "github.com/OpenListTeam/OpenList/v4/internal/conf"

// IsAuthRateLimitExceeded checks if the auth rate limit has been exceeded for the given IP.
// When maxRetries <= 0, rate limiting is disabled and this always returns false.
func IsAuthRateLimitExceeded(count int, maxRetries int) bool {
	return maxRetries > 0 && count >= maxRetries
}

// ShouldSkipAuthRateLimit returns true if the IP is whitelisted and rate limiting should be skipped.
func ShouldSkipAuthRateLimit(ip string) bool {
	return conf.IsIPWhitelisted(ip)
}

// IsIPBlocked returns true if the IP is in the blacklist and login should be denied.
func IsIPBlocked(ip string) bool {
	return conf.IsIPBlacklisted(ip)
}
