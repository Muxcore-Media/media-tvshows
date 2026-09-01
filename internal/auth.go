package internal

import (
	"net/http"
	"os"
	"strings"
)

func moduleTokenFromEnv() string {
	for _, k := range []string{"TVSHOWS_MODULE_TOKEN", "MUXCORE_MODULE_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func authorizeHTTPRequest(r *http.Request, moduleToken string) bool {
	if moduleToken == "" {
		return true
	}
	if tok := bearerToken(r.Header.Get("Authorization")); tok != "" && tok == moduleToken {
		return true
	}
	if tok := strings.TrimSpace(r.Header.Get("X-MuxCore-Module-Token")); tok != "" && tok == moduleToken {
		return true
	}
	return false
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	header = strings.TrimSpace(header)
	if len(header) > len(prefix) && strings.EqualFold(header[:len(prefix)], prefix) {
		return strings.TrimSpace(header[len(prefix):])
	}
	return header
}
