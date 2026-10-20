package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestRegistrationLimitedFeedback(t *testing.T) {
	for _, tc := range []struct {
		window time.Duration
		code   string
	}{{time.Hour, "rate_limited"}, {24 * time.Hour, "registration_daily_limited"}} {
		t.Run(tc.code, func(t *testing.T) {
			w := httptest.NewRecorder()
			registrationLimited(w, tc.window, tc.code)
			if w.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d", w.Code)
			}
			seconds, err := strconv.Atoi(w.Header().Get("Retry-After"))
			if err != nil || seconds < 1 || seconds > int(tc.window.Seconds()) {
				t.Fatalf("invalid Retry-After: %q", w.Header().Get("Retry-After"))
			}
			var body struct {
				Code string `json:"code"`
				Wait int    `json:"retry_after_seconds"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != tc.code || body.Wait != seconds {
				t.Fatalf("body = %+v", body)
			}
		})
	}
}

func TestRegistrationLimitWindowUsesLongestWait(t *testing.T) {
	for _, daily := range []bool{false, true} {
		window, code := registrationLimitWindow(daily)
		if daily && (window != 24*time.Hour || code != "registration_daily_limited") {
			t.Fatalf("daily limit hidden: %s %s", window, code)
		}
		if !daily && (window != time.Hour || code != "rate_limited") {
			t.Fatalf("unexpected hourly response: %s %s", window, code)
		}
	}
}

func TestRegistrationRequestLimitConfig(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"", 60}, {"120", 120}, {"0", 60}, {"-1", 60}, {"invalid", 60},
	} {
		t.Setenv("PPTS_AUTH_REGISTER_PER_HOUR", tc.raw)
		if got := defaultAuthRateConfig().registerPerHour; got != tc.want {
			t.Fatalf("env %q: got %d want %d", tc.raw, got, tc.want)
		}
	}
}
