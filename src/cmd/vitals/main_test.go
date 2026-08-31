package main

import "testing"

// TestDisplayAddr covers the address printed in the startup banner. It is the
// first thing a new user reads and the only instruction they are given, so an
// address that does not open is worse than no address at all.
func TestDisplayAddr(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{
			name: "a bare port becomes localhost",
			addr: ":8080",
			want: "localhost:8080",
		},
		{
			name: "the wildcard host is not clickable and becomes localhost",
			addr: "0.0.0.0:8080",
			want: "localhost:8080",
		},
		{
			name: "the IPv6 wildcard becomes localhost too",
			addr: "[::]:8080",
			want: "localhost:8080",
		},
		{
			name: "an explicit host is kept, because that is what was bound",
			addr: "127.0.0.1:8931",
			want: "127.0.0.1:8931",
		},
		{
			name: "a named host is kept",
			addr: "vitals.internal:9000",
			want: "vitals.internal:9000",
		},
		{
			name: "an IPv6 literal keeps its brackets",
			addr: "[::1]:8080",
			want: "[::1]:8080",
		},
		{
			name: "something that is not host:port is printed unchanged",
			addr: "not-an-address",
			want: "not-an-address",
		},
		{
			name: "the empty address is printed unchanged",
			addr: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displayAddr(tt.addr); got != tt.want {
				t.Errorf("displayAddr(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}
