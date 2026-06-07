package bootstrap

import "testing"

func TestWithSSLDisabled(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{
			in:   "postgresql://postgres:password@172.17.0.2:5432",
			want: "postgresql://postgres:password@172.17.0.2:5432?sslmode=disable",
		},
		{
			in:   "postgres://schick:schick_dev@localhost:5432/schick_db?sslmode=disable",
			want: "postgres://schick:schick_dev@localhost:5432/schick_db?sslmode=disable",
		},
		{
			in:   "host=localhost user=postgres password=secret",
			want: "host=localhost user=postgres password=secret sslmode=disable",
		},
	}

	for _, tc := range tests {
		if got := withSSLDisabled(tc.in); got != tc.want {
			t.Fatalf("withSSLDisabled(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
